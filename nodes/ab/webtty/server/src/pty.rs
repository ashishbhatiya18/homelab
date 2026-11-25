use crate::ssh::{SshPool, resolve_workspace_path, shell_quote};
use anyhow::{Context, Result, bail};
use chrono::{DateTime, Utc};
use russh::{ChannelMsg, ChannelWriteHalf};
use serde::{Deserialize, Serialize};
use std::collections::{HashMap, VecDeque};
use std::sync::Arc;
use tokio::sync::{Mutex, RwLock, broadcast};
use uuid::Uuid;

const SCROLLBACK_CAP_BYTES: usize = 256 * 1024;

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum SessionProfile {
    Claude,
    Agy,
    Shell,
}

impl SessionProfile {
    pub fn label(self) -> &'static str {
        match self {
            Self::Claude => "Claude",
            Self::Agy => "agy",
            Self::Shell => "Shell",
        }
    }

    fn remote_command(self, cwd: &str, tmux_session: Option<&str>) -> String {
        let quoted_cwd = shell_quote(cwd);
        let path_export = "export PATH=\"$HOME/.local/bin:/usr/local/bin:$PATH\"";
        let base_cmd = match self {
            Self::Claude => format!("{path_export} && exec claude"),
            Self::Agy => format!("{path_export} && exec agy"),
            Self::Shell => format!("{path_export} && exec \"${{SHELL:-/bin/bash}}\" -l"),
        };

        if let Some(name) = tmux_session {
            let quoted_name = shell_quote(name);
            let inner = format!("cd {quoted_cwd} && {base_cmd}");
            let quoted_inner = shell_quote(&inner);
            format!(
                "cd {quoted_cwd} && {path_export} && exec tmux new-session -A -s {quoted_name} \"sh -lc {quoted_inner}\""
            )
        } else {
            format!("cd {quoted_cwd} && {base_cmd}")
        }
    }
}

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionInfo {
    pub id: String,
    pub profile: SessionProfile,
    pub title: String,
    pub cwd: String,
    pub created_at: DateTime<Utc>,
    pub clients: usize,
    pub exited: bool,
}

#[derive(Clone)]
pub enum PtyEvent {
    Data(Vec<u8>),
    Exit(u32),
}

pub struct PtySession {
    pub id: String,
    pub profile: SessionProfile,
    pub title: String,
    pub cwd: String,
    pub created_at: DateTime<Utc>,
    writer: ChannelWriteHalf<russh::client::Msg>,
    scrollback: Mutex<VecDeque<u8>>,
    tx: broadcast::Sender<PtyEvent>,
    pub clients: Mutex<usize>,
    pub exited: Mutex<Option<u32>>,
}

impl PtySession {
    pub async fn write_input(&self, data: &[u8]) -> Result<()> {
        self.writer
            .data_bytes(data.to_vec())
            .await
            .context("SSH PTY write failed")
    }

    pub async fn resize(&self, cols: u32, rows: u32) -> Result<()> {
        self.writer
            .window_change(cols, rows, 0, 0)
            .await
            .context("SSH PTY resize failed")
    }

    pub async fn close(&self) -> Result<()> {
        let _ = self.writer.eof().await;
        let _ = self.writer.close().await;
        Ok(())
    }

    pub async fn snapshot_and_subscribe(
        &self,
    ) -> (Vec<u8>, Option<u32>, broadcast::Receiver<PtyEvent>) {
        let rx = self.tx.subscribe();
        let buf = self.scrollback.lock().await;
        let data: Vec<u8> = buf.iter().copied().collect();
        let exited = *self.exited.lock().await;
        (data, exited, rx)
    }

    pub async fn info(&self) -> SessionInfo {
        SessionInfo {
            id: self.id.clone(),
            profile: self.profile,
            title: self.title.clone(),
            cwd: self.cwd.clone(),
            created_at: self.created_at,
            clients: *self.clients.lock().await,
            exited: self.exited.lock().await.is_some(),
        }
    }
}

#[derive(Clone)]
pub struct SessionManager {
    ssh: SshPool,
    workspace_root: String,
    max_sessions: usize,
    sessions: Arc<RwLock<HashMap<String, Arc<PtySession>>>>,
}

impl SessionManager {
    pub fn new(ssh: SshPool, workspace_root: String, max_sessions: usize) -> Self {
        Self {
            ssh,
            workspace_root,
            max_sessions,
            sessions: Arc::new(RwLock::new(HashMap::new())),
        }
    }

    pub async fn list(&self) -> Vec<SessionInfo> {
        let map = self.sessions.read().await;
        let mut out = Vec::with_capacity(map.len());
        for s in map.values() {
            out.push(s.info().await);
        }
        out.sort_by_key(|s| s.created_at);
        out
    }

    pub async fn get(&self, id: &str) -> Option<Arc<PtySession>> {
        self.sessions.read().await.get(id).cloned()
    }

    pub async fn create(
        &self,
        profile: SessionProfile,
        requested_cwd: Option<&str>,
        use_tmux: bool,
    ) -> Result<SessionInfo> {
        if self.sessions.read().await.len() >= self.max_sessions {
            bail!("maximum concurrent sessions ({}) reached", self.max_sessions);
        }

        let sftp = self.ssh.open_sftp().await?;
        let cwd = resolve_workspace_path(&sftp, &self.workspace_root, requested_cwd).await?;
        let _ = sftp.close().await;

        let id = Uuid::new_v4().to_string();
        let short_id = &id[..8];
        let dir_label = cwd
            .rsplit('/')
            .next()
            .filter(|s| !s.is_empty())
            .unwrap_or("workspace");
        let title = format!("{} ({})", profile.label(), dir_label);
        let tmux_name = if use_tmux {
            Some(format!("webtty-{short_id}"))
        } else {
            None
        };

        let channel = self.ssh.open_channel().await?;
        channel
            .request_pty(true, "xterm-256color", 80, 24, 0, 0, &[])
            .await
            .context("failed to request PTY")?;

        let cmd = profile.remote_command(&cwd, tmux_name.as_deref());
        channel
            .exec(true, cmd.as_str())
            .await
            .context("failed to start remote PTY command")?;

        let (mut reader, writer) = channel.split();
        let (tx, _) = broadcast::channel(256);

        let session = Arc::new(PtySession {
            id: id.clone(),
            profile,
            title,
            cwd,
            created_at: Utc::now(),
            writer,
            scrollback: Mutex::new(VecDeque::with_capacity(64 * 1024)),
            tx,
            clients: Mutex::new(0),
            exited: Mutex::new(None),
        });

        self.sessions
            .write()
            .await
            .insert(id.clone(), Arc::clone(&session));

        let session_bg = Arc::clone(&session);
        tokio::spawn(async move {
            let mut exit_code = 0u32;
            while let Some(msg) = reader.wait().await {
                match msg {
                    ChannelMsg::Data { data } | ChannelMsg::ExtendedData { data, .. } => {
                        let bytes = data.to_vec();
                        {
                            let mut sb = session_bg.scrollback.lock().await;
                            for &b in &bytes {
                                if sb.len() >= SCROLLBACK_CAP_BYTES {
                                    sb.pop_front();
                                }
                                sb.push_back(b);
                            }
                        }
                        let _ = session_bg.tx.send(PtyEvent::Data(bytes));
                    }
                    ChannelMsg::ExitStatus { exit_status } => {
                        exit_code = exit_status;
                    }
                    ChannelMsg::Eof | ChannelMsg::Close => break,
                    _ => {}
                }
            }
            *session_bg.exited.lock().await = Some(exit_code);
            let _ = session_bg.tx.send(PtyEvent::Exit(exit_code));
        });

        Ok(session.info().await)
    }

    pub async fn remove(&self, id: &str) -> bool {
        let removed = self.sessions.write().await.remove(id);
        if let Some(session) = removed {
            let _ = session.close().await;
            true
        } else {
            false
        }
    }
}
