use crate::config::AppConfig;
use crate::ssh::{SshPool, resolve_workspace_path, shell_quote};
use axum::Json;
use axum::extract::{Query, State};
use axum::http::StatusCode;
use serde::{Deserialize, Serialize};

#[derive(Clone)]
pub struct GitState {
    pub cfg: AppConfig,
    pub ssh: SshPool,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GitRepo {
    pub name: String,
    pub path: String,
}

#[derive(Deserialize)]
pub struct RepoQuery {
    pub repo: String,
}

#[derive(Deserialize)]
pub struct DiffQuery {
    pub repo: String,
    pub mode: Option<String>,
    pub file: Option<String>,
}

#[derive(Deserialize)]
pub struct ShowQuery {
    pub repo: String,
    pub commit: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GitFileStatus {
    pub path: String,
    pub index_status: String,
    pub worktree_status: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GitStatusResponse {
    pub repo: String,
    pub branch: String,
    pub files: Vec<GitFileStatus>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GitCommit {
    pub hash: String,
    pub short_hash: String,
    pub subject: String,
    pub author: String,
    pub relative_date: String,
}

pub async fn list_repos(
    State(st): State<GitState>,
) -> Result<Json<Vec<GitRepo>>, (StatusCode, String)> {
    let sftp = st.ssh.open_sftp().await.map_err(internal_err)?;
    let root = resolve_workspace_path(&sftp, &st.cfg.workspace_root, None)
        .await
        .map_err(bad_req)?;
    let _ = sftp.close().await;

    let quoted_root = shell_quote(&root);
    let cmd = format!(
        "find {quoted_root} -maxdepth 3 -name .git -exec dirname {{}} \\; 2>/dev/null | sort"
    );
    let (_code, stdout, _stderr) = st.ssh.exec_capture(&cmd).await.map_err(internal_err)?;

    let root_prefix = format!("{}/", root.trim_end_matches('/'));
    let repos = stdout
        .lines()
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .map(|path| {
            let name = path
                .strip_prefix(&root_prefix)
                .unwrap_or(path)
                .to_string();
            GitRepo {
                name,
                path: path.to_string(),
            }
        })
        .collect();

    Ok(Json(repos))
}

pub async fn status(
    State(st): State<GitState>,
    Query(q): Query<RepoQuery>,
) -> Result<Json<GitStatusResponse>, (StatusCode, String)> {
    let repo = resolve_repo(&st, &q.repo).await?;
    let cmd = format!(
        "git -C {} status --porcelain=v1 -b",
        shell_quote(&repo)
    );
    let (code, stdout, stderr) = st.ssh.exec_capture(&cmd).await.map_err(internal_err)?;
    if code != 0 {
        return Err((StatusCode::BAD_REQUEST, stderr));
    }

    let mut branch = String::new();
    let mut files = Vec::new();
    for line in stdout.lines() {
        if let Some(b) = line.strip_prefix("## ") {
            branch = b.to_string();
            continue;
        }
        if line.len() < 4 {
            continue;
        }
        let idx = line[0..1].to_string();
        let wt = line[1..2].to_string();
        let path = line[3..].trim().to_string();
        files.push(GitFileStatus {
            path,
            index_status: idx,
            worktree_status: wt,
        });
    }

    Ok(Json(GitStatusResponse {
        repo,
        branch,
        files,
    }))
}

pub async fn diff(
    State(st): State<GitState>,
    Query(q): Query<DiffQuery>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let repo = resolve_repo(&st, &q.repo).await?;
    let mode = q.mode.as_deref().unwrap_or("all");

    let file_arg = match q.file.as_deref().map(str::trim).filter(|s| !s.is_empty()) {
        Some(f) => {
            if f.starts_with('-') || f.contains('\0') {
                return Err((StatusCode::BAD_REQUEST, "invalid file path".into()));
            }
            Some(f.to_string())
        }
        None => None,
    };

    let quoted_repo = shell_quote(&repo);
    let cmd = match (mode, file_arg.as_deref()) {
        ("staged", Some(f)) => format!("git -C {quoted_repo} diff --cached -- {}", shell_quote(f)),
        ("staged", None) => format!("git -C {quoted_repo} diff --cached"),
        ("unstaged", Some(f)) => format!("git -C {quoted_repo} diff -- {}", shell_quote(f)),
        ("unstaged", None) => format!("git -C {quoted_repo} diff"),
        (_, Some(f)) => format!("git -C {quoted_repo} diff HEAD -- {}", shell_quote(f)),
        (_, None) => format!("git -C {quoted_repo} diff HEAD"),
    };

    let (code, mut stdout, stderr) = st.ssh.exec_capture(&cmd).await.map_err(internal_err)?;
    if code != 0 && code != 1 {
        return Err((StatusCode::BAD_REQUEST, stderr));
    }

    // If an untracked file was selected in unstaged mode, synthesize a unified diff.
    if mode == "unstaged" && stdout.is_empty() {
        if let Some(f) = file_arg.as_deref() {
            let qf = shell_quote(f);
            let check_untracked = format!(
                "git -C {quoted_repo} ls-files --others --exclude-standard -- {qf}"
            );
            if let Ok((0, ls_out, _)) = st.ssh.exec_capture(&check_untracked).await {
                if !ls_out.trim().is_empty() {
                    let no_idx = format!("git -C {quoted_repo} diff --no-index -- /dev/null {qf}");
                    if let Ok((c2, d2, _)) = st.ssh.exec_capture(&no_idx).await {
                        if c2 == 0 || c2 == 1 {
                            stdout = d2;
                        }
                    }
                }
            }
        }
    }

    Ok(Json(serde_json::json!({ "diff": stdout })))
}

pub async fn log(
    State(st): State<GitState>,
    Query(q): Query<RepoQuery>,
) -> Result<Json<Vec<GitCommit>>, (StatusCode, String)> {
    let repo = resolve_repo(&st, &q.repo).await?;
    let cmd = format!(
        "git -C {} log -n 25 --format=%H%x1f%h%x1f%s%x1f%an%x1f%cr",
        shell_quote(&repo)
    );
    let (code, stdout, stderr) = st.ssh.exec_capture(&cmd).await.map_err(internal_err)?;
    if code != 0 {
        return Err((StatusCode::BAD_REQUEST, stderr));
    }

    let commits = stdout
        .lines()
        .filter_map(|line| {
            let parts: Vec<&str> = line.split('\x1f').collect();
            if parts.len() == 5 {
                Some(GitCommit {
                    hash: parts[0].to_string(),
                    short_hash: parts[1].to_string(),
                    subject: parts[2].to_string(),
                    author: parts[3].to_string(),
                    relative_date: parts[4].to_string(),
                })
            } else {
                None
            }
        })
        .collect();

    Ok(Json(commits))
}

pub async fn show(
    State(st): State<GitState>,
    Query(q): Query<ShowQuery>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let repo = resolve_repo(&st, &q.repo).await?;
    let sha = q.commit.trim();
    if !(4..=40).contains(&sha.len()) || !sha.chars().all(|c| c.is_ascii_hexdigit()) {
        return Err((StatusCode::BAD_REQUEST, "invalid commit hash".into()));
    }

    let cmd = format!("git -C {} show --format=fuller {}", shell_quote(&repo), sha);
    let (code, stdout, stderr) = st.ssh.exec_capture(&cmd).await.map_err(internal_err)?;
    if code != 0 {
        return Err((StatusCode::BAD_REQUEST, stderr));
    }

    Ok(Json(serde_json::json!({ "diff": stdout })))
}

async fn resolve_repo(st: &GitState, repo_path: &str) -> Result<String, (StatusCode, String)> {
    let sftp = st.ssh.open_sftp().await.map_err(internal_err)?;
    let resolved = resolve_workspace_path(&sftp, &st.cfg.workspace_root, Some(repo_path))
        .await
        .map_err(bad_req)?;
    let _ = sftp.close().await;
    Ok(resolved)
}

fn bad_req(e: impl std::fmt::Display) -> (StatusCode, String) {
    (StatusCode::BAD_REQUEST, e.to_string())
}

fn internal_err(e: impl std::fmt::Display) -> (StatusCode, String) {
    (StatusCode::INTERNAL_SERVER_ERROR, e.to_string())
}
