use crate::config::AppConfig;
use anyhow::{Context, Result, bail};
use russh::client;
use russh::keys::{HashAlg, PrivateKeyWithHashAlg, PublicKeyOrCertificate, load_secret_key};
use russh::{Channel, ChannelMsg};
use russh_sftp::client::SftpSession;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::Mutex;
use tracing::{info, warn};

const EXEC_TIMEOUT: Duration = Duration::from_secs(20);
const MAX_EXEC_OUTPUT_BYTES: usize = 2 * 1024 * 1024;

pub struct SshHandler {
    expected_fingerprint: Option<String>,
    allow_insecure: bool,
}

impl client::Handler for SshHandler {
    type Error = russh::Error;

    async fn check_server_key(
        &mut self,
        server_public_key: &PublicKeyOrCertificate,
    ) -> Result<bool, Self::Error> {
        let actual = match server_public_key {
            PublicKeyOrCertificate::PublicKey { key, .. } => {
                key.fingerprint(HashAlg::Sha256).to_string()
            }
            PublicKeyOrCertificate::Certificate(cert) => {
                cert.public_key().fingerprint(HashAlg::Sha256).to_string()
            }
        };
        if let Some(expected) = &self.expected_fingerprint {
            let norm_expected = expected.strip_prefix("SHA256:").unwrap_or(expected);
            let norm_actual = actual.strip_prefix("SHA256:").unwrap_or(&actual);
            if norm_expected == norm_actual {
                return Ok(true);
            }
            warn!(expected = %expected, actual = %actual, "SSH host key fingerprint mismatch");
            return Ok(false);
        }

        if self.allow_insecure {
            warn!(fingerprint = %actual, "SSH_HOST_FINGERPRINT unset; accepting host key because SSH_ALLOW_INSECURE_HOST_KEY=true");
            return Ok(true);
        }

        warn!(fingerprint = %actual, "rejecting SSH host key: set SSH_HOST_FINGERPRINT to this value");
        Ok(false)
    }
}

#[derive(Clone)]
pub struct SshPool {
    cfg: AppConfig,
    handle: Arc<Mutex<Option<Arc<client::Handle<SshHandler>>>>>,
}

impl SshPool {
    pub fn new(cfg: AppConfig) -> Self {
        Self {
            cfg,
            handle: Arc::new(Mutex::new(None)),
        }
    }

    pub async fn handle(&self) -> Result<Arc<client::Handle<SshHandler>>> {
        let mut guard = self.handle.lock().await;
        if let Some(existing) = guard.as_ref() {
            if !existing.is_closed() {
                return Ok(Arc::clone(existing));
            }
        }

        let connected = self.connect_new().await?;
        let arc = Arc::new(connected);
        *guard = Some(Arc::clone(&arc));
        Ok(arc)
    }

    async fn connect_new(&self) -> Result<client::Handle<SshHandler>> {
        let key_pair = load_secret_key(&self.cfg.ssh_key_path, None)
            .with_context(|| format!("failed to load SSH key at {}", self.cfg.ssh_key_path.display()))?;

        let ssh_cfg = Arc::new(client::Config {
            keepalive_interval: Some(Duration::from_secs(30)),
            keepalive_max: 3,
            ..Default::default()
        });

        let handler = SshHandler {
            expected_fingerprint: self.cfg.ssh_host_fingerprint.clone(),
            allow_insecure: self.cfg.ssh_allow_insecure_host_key,
        };

        let addr = format!("{}:{}", self.cfg.ssh_host, self.cfg.ssh_port);
        let mut session = client::connect(ssh_cfg, addr.as_str(), handler)
            .await
            .with_context(|| format!("failed to connect SSH to {addr}"))?;

        let preferred_hash = session.best_supported_rsa_hash().await?.flatten();
        let auth_res = session
            .authenticate_publickey(
                &self.cfg.ssh_user,
                PrivateKeyWithHashAlg::new(Arc::new(key_pair), preferred_hash),
            )
            .await
            .context("SSH public key auth failed")?;

        if !auth_res.success() {
            bail!("SSH authentication rejected for user {}", self.cfg.ssh_user);
        }

        info!(host = %addr, user = %self.cfg.ssh_user, "connected SSH session");
        Ok(session)
    }

    pub async fn open_channel(&self) -> Result<Channel<client::Msg>> {
        let h = self.handle().await?;
        match h.channel_open_session().await {
            Ok(ch) => Ok(ch),
            Err(_) => {
                // Force reconnect once on stale handle
                *self.handle.lock().await = None;
                let h = self.handle().await?;
                h.channel_open_session()
                    .await
                    .context("failed to open SSH session channel")
            }
        }
    }

    pub async fn open_sftp(&self) -> Result<SftpSession> {
        let channel = self.open_channel().await?;
        channel
            .request_subsystem(true, "sftp")
            .await
            .context("failed to start SFTP subsystem")?;
        SftpSession::new(channel.into_stream())
            .await
            .context("failed to initialize SFTP session")
    }

    pub async fn exec_capture(&self, command: &str) -> Result<(u32, String, String)> {
        let mut channel = self.open_channel().await?;
        channel
            .exec(true, command)
            .await
            .context("failed to exec SSH command")?;

        let mut stdout = Vec::new();
        let mut stderr = Vec::new();
        let mut exit_code: u32 = 0;

        let res = tokio::time::timeout(EXEC_TIMEOUT, async {
            while let Some(msg) = channel.wait().await {
                match msg {
                    ChannelMsg::Data { data } => {
                        append_capped(&mut stdout, &data, MAX_EXEC_OUTPUT_BYTES);
                    }
                    ChannelMsg::ExtendedData { data, ext } if ext == 1 => {
                        append_capped(&mut stderr, &data, MAX_EXEC_OUTPUT_BYTES);
                    }
                    ChannelMsg::ExitStatus { exit_status } => {
                        exit_code = exit_status;
                    }
                    ChannelMsg::Eof => {}
                    _ => {}
                }
            }
        })
        .await;

        if res.is_err() {
            let _ = channel.close().await;
            bail!("remote command timed out");
        }

        Ok((
            exit_code,
            String::from_utf8_lossy(&stdout).into_owned(),
            String::from_utf8_lossy(&stderr).into_owned(),
        ))
    }
}

fn append_capped(dst: &mut Vec<u8>, src: &[u8], cap: usize) {
    if dst.len() >= cap {
        return;
    }
    let remaining = cap - dst.len();
    let take = remaining.min(src.len());
    dst.extend_from_slice(&src[..take]);
}

/// Strictly quotes an argument for POSIX sh.
pub fn shell_quote(arg: &str) -> String {
    let mut out = String::with_capacity(arg.len() + 2);
    out.push('\'');
    for ch in arg.chars() {
        if ch == '\'' {
            out.push_str("'\"'\"'");
        } else {
            out.push(ch);
        }
    }
    out.push('\'');
    out
}

/// Resolves `requested` inside `workspace_root` via SFTP canonicalize to prevent
/// both `..` path traversal and symlinks escaping the workspace root.
pub async fn resolve_workspace_path(
    sftp: &SftpSession,
    workspace_root: &str,
    requested: Option<&str>,
) -> Result<String> {
    let canonical_root = sftp
        .canonicalize(workspace_root)
        .await
        .with_context(|| format!("workspace root unavailable: {workspace_root}"))?;

    let target = match requested {
        Some(p) if !p.trim().is_empty() => p.trim(),
        _ => return Ok(canonical_root),
    };

    let candidate = if target.starts_with('/') {
        target.to_string()
    } else {
        format!("{}/{}", canonical_root.trim_end_matches('/'), target)
    };

    let resolved = sftp
        .canonicalize(&candidate)
        .await
        .with_context(|| format!("path not found: {target}"))?;

    let root_prefix = format!("{}/", canonical_root.trim_end_matches('/'));
    if resolved != canonical_root && !resolved.starts_with(&root_prefix) {
        bail!("path escapes workspace root");
    }

    Ok(resolved)
}

#[cfg(test)]
mod tests {
    use super::shell_quote;

    #[test]
    fn quotes_posix_metacharacters_safely() {
        assert_eq!(shell_quote("simple"), "'simple'");
        assert_eq!(shell_quote("a'b"), "'a'\"'\"'b'");
        assert_eq!(shell_quote("$(rm -rf /)"), "'$(rm -rf /)'");
    }
}
