mod config;
mod fs_api;
mod git_api;
mod oauth_api;
mod pty;
mod ssh;

use axum::body::Body;
use axum::extract::ws::{Message, WebSocket};
use axum::extract::{Path, State, WebSocketUpgrade};
use axum::http::{HeaderMap, HeaderValue, Request, StatusCode, header};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::{delete, get, post, put};
use axum::{Json, Router};
use config::AppConfig;
use fs_api::FsState;
use futures_util::{SinkExt, StreamExt};
use git_api::GitState;
use oauth_api::OAuthState;
use pty::{PtyEvent, SessionManager, SessionProfile};
use rust_embed::Embed;
use serde::Deserialize;
use ssh::SshPool;
use std::net::SocketAddr;
use tracing::{info, warn};

#[derive(Embed)]
#[folder = "../web/dist"]
struct Assets;

#[derive(Clone)]
struct AppState {
    cfg: AppConfig,
    sessions: SessionManager,
}

#[derive(Deserialize)]
struct CreateSessionReq {
    profile: SessionProfile,
    cwd: Option<String>,
    #[serde(default)]
    tmux: bool,
}

#[derive(Deserialize)]
#[serde(tag = "type", rename_all = "lowercase")]
enum WsClientMsg {
    Data { data: String },
    Resize { cols: u32, rows: u32 },
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "webtty=info".into()),
        )
        .init();

    let cfg = AppConfig::from_env()?;
    let ssh = SshPool::new(cfg.clone());
    let sessions = SessionManager::new(
        ssh.clone(),
        cfg.workspace_root.clone(),
        cfg.max_concurrent_sessions,
    );

    let app_state = AppState {
        cfg: cfg.clone(),
        sessions,
    };
    let fs_state = FsState {
        cfg: cfg.clone(),
        ssh: ssh.clone(),
    };
    let git_state = GitState {
        cfg: cfg.clone(),
        ssh: ssh.clone(),
    };
    let oauth_state = OAuthState {
        cfg: cfg.clone(),
        ssh,
    };

    let fs_routes = Router::new()
        .route("/list", get(fs_api::list_dir))
        .route("/read", get(fs_api::read_file))
        .route("/write", put(fs_api::write_file))
        .with_state(fs_state);

    let git_routes = Router::new()
        .route("/repos", get(git_api::list_repos))
        .route("/status", get(git_api::status))
        .route("/diff", get(git_api::diff))
        .route("/log", get(git_api::log))
        .route("/show", get(git_api::show))
        .with_state(git_state);

    let oauth_routes = Router::new()
        .route("/relay", post(oauth_api::relay))
        .with_state(oauth_state);

    let app = Router::new()
        .route("/healthz", get(|| async { "ok" }))
        .route("/api/config", get(get_config))
        .route("/api/sessions", get(list_sessions).post(create_session))
        .route("/api/sessions/{id}", delete(delete_session))
        .route("/ws/{id}", get(ws_handler))
        .with_state(app_state)
        .nest("/api/fs", fs_routes)
        .nest("/api/git", git_routes)
        .nest("/api/oauth", oauth_routes)
        .fallback(serve_static)
        .layer(middleware::from_fn(security_headers));

    let addr = SocketAddr::from(([0, 0, 0, 0], cfg.port));
    info!(%addr, ssh_host = %cfg.ssh_host, workspace = %cfg.workspace_root, "webtty listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app).await?;
    Ok(())
}

async fn security_headers(req: Request<Body>, next: Next) -> Response {
    let mut res = next.run(req).await;
    let headers = res.headers_mut();
    headers.insert("X-Content-Type-Options", HeaderValue::from_static("nosniff"));
    headers.insert("X-Frame-Options", HeaderValue::from_static("DENY"));
    headers.insert("Referrer-Policy", HeaderValue::from_static("no-referrer"));
    headers.insert("Cache-Control", HeaderValue::from_static("no-store"));
    headers.insert(
        "Content-Security-Policy",
        HeaderValue::from_static(
            "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' wss: ws:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
        ),
    );
    res
}

async fn get_config(State(st): State<AppState>) -> Json<serde_json::Value> {
    Json(serde_json::json!({
        "workspaceRoot": st.cfg.workspace_root,
        "allowFileWrite": st.cfg.allow_file_write,
        "allowOAuthRelay": st.cfg.allow_oauth_relay,
    }))
}

async fn list_sessions(State(st): State<AppState>) -> Json<Vec<pty::SessionInfo>> {
    Json(st.sessions.list().await)
}

async fn create_session(
    State(st): State<AppState>,
    Json(req): Json<CreateSessionReq>,
) -> Result<Json<pty::SessionInfo>, (StatusCode, String)> {
    let info = st
        .sessions
        .create(req.profile, req.cwd.as_deref(), req.tmux)
        .await
        .map_err(|e| (StatusCode::BAD_REQUEST, e.to_string()))?;
    Ok(Json(info))
}

async fn delete_session(
    State(st): State<AppState>,
    Path(id): Path<String>,
) -> StatusCode {
    if st.sessions.remove(&id).await {
        StatusCode::NO_CONTENT
    } else {
        StatusCode::NOT_FOUND
    }
}

fn is_allowed_origin(headers: &HeaderMap, allowed: &[String]) -> bool {
    let origin = match headers.get(header::ORIGIN).and_then(|v| v.to_str().ok()) {
        Some(o) => o,
        None => return false,
    };
    if !allowed.is_empty() {
        return allowed.iter().any(|a| a == origin);
    }
    let host = match headers.get(header::HOST).and_then(|v| v.to_str().ok()) {
        Some(h) => h,
        None => return false,
    };
    origin == format!("https://{host}") || origin == format!("http://{host}")
}

async fn ws_handler(
    State(st): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
    ws: WebSocketUpgrade,
) -> Response {
    if !is_allowed_origin(&headers, &st.cfg.allowed_origins) {
        warn!("rejected websocket with disallowed origin");
        return (StatusCode::FORBIDDEN, "forbidden origin").into_response();
    }

    let Some(session) = st.sessions.get(&id).await else {
        return (StatusCode::NOT_FOUND, "session not found").into_response();
    };

    let max_payload = st.cfg.max_ws_payload_bytes;
    ws.max_message_size(max_payload)
        .on_upgrade(move |socket| handle_ws(socket, session))
}

async fn handle_ws(socket: WebSocket, session: std::sync::Arc<pty::PtySession>) {
    *session.clients.lock().await += 1;
    let (mut ws_tx, mut ws_rx) = socket.split();
    let (replay, exited, mut rx) = session.snapshot_and_subscribe().await;

    if !replay.is_empty() {
        let msg = serde_json::json!({
            "type": "replay",
            "data": String::from_utf8_lossy(&replay),
        });
        if ws_tx.send(Message::Text(msg.to_string().into())).await.is_err() {
            *session.clients.lock().await -= 1;
            return;
        }
    }

    if let Some(code) = exited {
        let msg = serde_json::json!({ "type": "exit", "code": code });
        let _ = ws_tx.send(Message::Text(msg.to_string().into())).await;
    }

    let send_task = tokio::spawn(async move {
        while let Ok(ev) = rx.recv().await {
            let payload = match ev {
                PtyEvent::Data(bytes) => serde_json::json!({
                    "type": "data",
                    "data": String::from_utf8_lossy(&bytes),
                }),
                PtyEvent::Exit(code) => serde_json::json!({
                    "type": "exit",
                    "code": code,
                }),
            };
            if ws_tx
                .send(Message::Text(payload.to_string().into()))
                .await
                .is_err()
            {
                break;
            }
        }
    });

    while let Some(Ok(msg)) = ws_rx.next().await {
        if let Message::Text(txt) = msg {
            if let Ok(parsed) = serde_json::from_str::<WsClientMsg>(&txt) {
                match parsed {
                    WsClientMsg::Data { data } => {
                        let _ = session.write_input(data.as_bytes()).await;
                    }
                    WsClientMsg::Resize { cols, rows } => {
                        if (1..=1000).contains(&cols) && (1..=1000).contains(&rows) {
                            let _ = session.resize(cols, rows).await;
                        }
                    }
                }
            }
        }
    }

    send_task.abort();
    let mut c = session.clients.lock().await;
    *c = c.saturating_sub(1);
}

async fn serve_static(req: Request<Body>) -> Response {
    let raw_path = req.uri().path().trim_start_matches('/');
    let path = if raw_path.is_empty() {
        "index.html"
    } else {
        raw_path
    };

    let asset = Assets::get(path).or_else(|| Assets::get("index.html"));
    match asset {
        Some(file) => {
            let mime = mime_guess::from_path(if Assets::get(path).is_some() {
                path
            } else {
                "index.html"
            })
            .first_or_octet_stream();
            Response::builder()
                .status(StatusCode::OK)
                .header(header::CONTENT_TYPE, mime.as_ref())
                .body(Body::from(file.data.into_owned()))
                .unwrap()
        }
        None => (StatusCode::NOT_FOUND, "not found").into_response(),
    }
}

#[cfg(test)]
mod tests {
    use super::is_allowed_origin;
    use axum::http::{HeaderMap, HeaderValue, header};

    #[test]
    fn validates_same_host_and_explicit_origins() {
        let mut h = HeaderMap::new();
        h.insert(header::HOST, HeaderValue::from_static("term2.ab18.in"));
        h.insert(header::ORIGIN, HeaderValue::from_static("https://term2.ab18.in"));
        assert!(is_allowed_origin(&h, &[]));

        h.insert(header::ORIGIN, HeaderValue::from_static("null"));
        assert!(!is_allowed_origin(&h, &[]));

        h.insert(header::ORIGIN, HeaderValue::from_static("https://evil.example"));
        assert!(!is_allowed_origin(&h, &[]));
        assert!(is_allowed_origin(&h, &["https://evil.example".to_string()]));
    }
}
