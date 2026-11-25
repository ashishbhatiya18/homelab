use crate::config::AppConfig;
use crate::ssh::{SshPool, shell_quote};
use axum::Json;
use axum::extract::State;
use axum::http::StatusCode;
use serde::Deserialize;
use url::Url;

#[derive(Clone)]
pub struct OAuthState {
    pub cfg: AppConfig,
    pub ssh: SshPool,
}

#[derive(Deserialize)]
pub struct RelayRequest {
    pub url: String,
}

pub fn validate_oauth_callback_url(raw: &str) -> Result<String, (StatusCode, String)> {
    let parsed = Url::parse(raw.trim())
        .map_err(|e| (StatusCode::BAD_REQUEST, format!("invalid URL: {e}")))?;

    if parsed.scheme() != "http" {
        return Err((StatusCode::BAD_REQUEST, "only http:// callbacks allowed".into()));
    }
    if !parsed.username().is_empty() || parsed.password().is_some() {
        return Err((StatusCode::BAD_REQUEST, "userinfo not allowed in URL".into()));
    }
    match parsed.host_str() {
        Some("localhost") | Some("127.0.0.1") => {}
        _ => {
            return Err((
                StatusCode::BAD_REQUEST,
                "only localhost / 127.0.0.1 OAuth callbacks are permitted".into(),
            ));
        }
    }
    let port = parsed
        .port()
        .filter(|&p| p >= 1024)
        .ok_or((StatusCode::BAD_REQUEST, "callback port must be >= 1024".to_string()))?;

    let path_and_query = match parsed.query() {
        Some(q) => format!("{}?{}", parsed.path(), q),
        None => parsed.path().to_string(),
    };
    Ok(format!("http://127.0.0.1:{port}{path_and_query}"))
}

pub async fn relay(
    State(st): State<OAuthState>,
    Json(req): Json<RelayRequest>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    if !st.cfg.allow_oauth_relay {
        return Err((StatusCode::FORBIDDEN, "OAuth relay is disabled".into()));
    }

    let target = validate_oauth_callback_url(&req.url)?;
    let quoted_target = shell_quote(&target);

    // Deliver strictly to loopback on the SSH host; no shell expansion.
    let cmd = format!(
        "curl -sS --max-time 8 -w '\\n%{{http_code}}' {quoted_target} 2>&1"
    );
    let (code, stdout, stderr) = st
        .ssh
        .exec_capture(&cmd)
        .await
        .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?;

    if code != 0 {
        return Err((
            StatusCode::BAD_GATEWAY,
            format!("callback delivery failed (exit {code}): {stdout} {stderr}"),
        ));
    }

    let mut lines: Vec<&str> = stdout.trim_end().lines().collect();
    let http_status = lines
        .pop()
        .and_then(|s| s.parse::<u16>().ok())
        .unwrap_or(200);
    let body = lines.join("\n");

    Ok(Json(serde_json::json!({
        "status": http_status,
        "body": body,
    })))
}

#[cfg(test)]
mod tests {
    use super::validate_oauth_callback_url;

    #[test]
    fn accepts_valid_localhost_callback() {
        let out = validate_oauth_callback_url(
            "http://localhost:54321/callback?code=abc&state=xyz",
        )
        .unwrap();
        assert_eq!(out, "http://127.0.0.1:54321/callback?code=abc&state=xyz");
    }

    #[test]
    fn rejects_non_localhost_or_privileged_ports() {
        assert!(validate_oauth_callback_url("https://localhost:54321/cb").is_err());
        assert!(validate_oauth_callback_url("http://169.254.169.254:8080/latest").is_err());
        assert!(validate_oauth_callback_url("http://localhost:22/").is_err());
        assert!(validate_oauth_callback_url("http://user:pass@localhost:54321/cb").is_err());
    }
}
