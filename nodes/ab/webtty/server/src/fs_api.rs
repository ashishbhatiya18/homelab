use crate::config::AppConfig;
use crate::ssh::{SshPool, resolve_workspace_path};
use axum::Json;
use axum::extract::{Query, State};
use axum::http::StatusCode;
use serde::{Deserialize, Serialize};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

const MAX_FILE_READ_BYTES: usize = 512 * 1024;

#[derive(Clone)]
pub struct FsState {
    pub cfg: AppConfig,
    pub ssh: SshPool,
}

#[derive(Deserialize)]
pub struct PathQuery {
    pub path: Option<String>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct FsEntry {
    pub name: String,
    pub path: String,
    pub is_dir: bool,
    pub size: u64,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct FsListResponse {
    pub root: String,
    pub path: String,
    pub entries: Vec<FsEntry>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct FsReadResponse {
    pub path: String,
    pub content: String,
    pub size: u64,
    pub truncated: bool,
    pub binary: bool,
}

#[derive(Deserialize)]
pub struct WriteReq {
    pub path: String,
    pub content: String,
}

pub async fn list_dir(
    State(st): State<FsState>,
    Query(q): Query<PathQuery>,
) -> Result<Json<FsListResponse>, (StatusCode, String)> {
    let sftp = st.ssh.open_sftp().await.map_err(internal_err)?;
    let root = resolve_workspace_path(&sftp, &st.cfg.workspace_root, None)
        .await
        .map_err(bad_req)?;
    let resolved = resolve_workspace_path(&sftp, &st.cfg.workspace_root, q.path.as_deref())
        .await
        .map_err(bad_req)?;

    let raw_entries = sftp.read_dir(&resolved).await.map_err(bad_req)?;
    let mut entries = Vec::new();
    for entry in raw_entries {
        let name = entry.file_name();
        if name == "." || name == ".." || name == ".git" {
            continue;
        }
        let meta = entry.metadata();
        let is_dir = meta.is_dir();
        let size = meta.size.unwrap_or(0);
        let full = format!("{}/{}", resolved.trim_end_matches('/'), name);
        entries.push(FsEntry {
            name,
            path: full,
            is_dir,
            size,
        });
    }

    entries.sort_by(|a, b| {
        b.is_dir
            .cmp(&a.is_dir)
            .then_with(|| a.name.to_lowercase().cmp(&b.name.to_lowercase()))
    });

    let _ = sftp.close().await;
    Ok(Json(FsListResponse {
        root,
        path: resolved,
        entries,
    }))
}

pub async fn read_file(
    State(st): State<FsState>,
    Query(q): Query<PathQuery>,
) -> Result<Json<FsReadResponse>, (StatusCode, String)> {
    let sftp = st.ssh.open_sftp().await.map_err(internal_err)?;
    let resolved = resolve_workspace_path(&sftp, &st.cfg.workspace_root, q.path.as_deref())
        .await
        .map_err(bad_req)?;

    let meta = sftp.metadata(&resolved).await.map_err(bad_req)?;
    if meta.is_dir() {
        return Err((StatusCode::BAD_REQUEST, "cannot read a directory".into()));
    }
    let total_size = meta.size.unwrap_or(0);

    let mut file = sftp.open(&resolved).await.map_err(bad_req)?;
    let mut buf = vec![0u8; (total_size as usize).min(MAX_FILE_READ_BYTES + 1)];
    let mut read_total = 0usize;
    while read_total < buf.len() {
        let n = file.read(&mut buf[read_total..]).await.map_err(internal_err)?;
        if n == 0 {
            break;
        }
        read_total += n;
    }
    let _ = sftp.close().await;

    let truncated = read_total > MAX_FILE_READ_BYTES || total_size as usize > MAX_FILE_READ_BYTES;
    let slice = &buf[..read_total.min(MAX_FILE_READ_BYTES)];
    let binary = slice.iter().take(4096).any(|&b| b == 0);
    let content = if binary {
        String::new()
    } else {
        String::from_utf8_lossy(slice).into_owned()
    };

    Ok(Json(FsReadResponse {
        path: resolved,
        content,
        size: total_size,
        truncated,
        binary,
    }))
}

pub async fn write_file(
    State(st): State<FsState>,
    Json(req): Json<WriteReq>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    if !st.cfg.allow_file_write {
        return Err((
            StatusCode::FORBIDDEN,
            "file editing is disabled (ALLOW_FILE_WRITE=false)".into(),
        ));
    }
    if req.content.len() > MAX_FILE_READ_BYTES {
        return Err((StatusCode::PAYLOAD_TOO_LARGE, "file exceeds max write size".into()));
    }

    let sftp = st.ssh.open_sftp().await.map_err(internal_err)?;
    let resolved = resolve_workspace_path(&sftp, &st.cfg.workspace_root, Some(&req.path))
        .await
        .map_err(bad_req)?;

    let mut file = sftp.create(&resolved).await.map_err(internal_err)?;
    file.write_all(req.content.as_bytes())
        .await
        .map_err(internal_err)?;
    file.shutdown().await.ok();
    let _ = sftp.close().await;

    Ok(Json(serde_json::json!({ "ok": true })))
}

fn bad_req(e: impl std::fmt::Display) -> (StatusCode, String) {
    (StatusCode::BAD_REQUEST, e.to_string())
}

fn internal_err(e: impl std::fmt::Display) -> (StatusCode, String) {
    (StatusCode::INTERNAL_SERVER_ERROR, e.to_string())
}
