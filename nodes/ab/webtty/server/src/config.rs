use anyhow::{Context, Result};
use std::env;
use std::path::PathBuf;

#[derive(Clone, Debug)]
pub struct AppConfig {
    pub port: u16,
    pub ssh_host: String,
    pub ssh_port: u16,
    pub ssh_user: String,
    pub ssh_key_path: PathBuf,
    pub ssh_host_fingerprint: Option<String>,
    pub ssh_allow_insecure_host_key: bool,
    pub workspace_root: String,
    pub max_concurrent_sessions: usize,
    pub max_ws_payload_bytes: usize,
    pub allowed_origins: Vec<String>,
    pub allow_file_write: bool,
    pub allow_oauth_relay: bool,
}

impl AppConfig {
    pub fn from_env() -> Result<Self> {
        let port = env_parse("PORT", 7681u16)?;
        let ssh_host = env::var("SSH_HOST").unwrap_or_else(|_| "11.11.11.1".to_string());
        let ssh_port = env_parse("SSH_PORT", 22u16)?;
        let ssh_user = env::var("SSH_USER").unwrap_or_else(|_| "webtty".to_string());
        let ssh_key_path = PathBuf::from(
            env::var("SSH_KEY_FILE").unwrap_or_else(|_| "/run/secrets/ssh_key".to_string()),
        );
        let ssh_host_fingerprint = env::var("SSH_HOST_FINGERPRINT")
            .ok()
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty());
        let ssh_allow_insecure_host_key = env_bool("SSH_ALLOW_INSECURE_HOST_KEY", false);
        let workspace_root = env::var("WORKSPACE_ROOT")
            .unwrap_or_else(|_| "/home/dietpi/repos".to_string())
            .trim_end_matches('/')
            .to_string();
        let max_concurrent_sessions = env_parse("MAX_CONCURRENT_SESSIONS", 8usize)?;
        let max_ws_payload_bytes = env_parse("MAX_WS_PAYLOAD_BYTES", 64 * 1024usize)?;
        let allowed_origins = env::var("ALLOWED_ORIGINS")
            .unwrap_or_default()
            .split(',')
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
            .collect();
        let allow_file_write = env_bool("ALLOW_FILE_WRITE", false);
        let allow_oauth_relay = env_bool("ALLOW_OAUTH_RELAY", true);

        Ok(Self {
            port,
            ssh_host,
            ssh_port,
            ssh_user,
            ssh_key_path,
            ssh_host_fingerprint,
            ssh_allow_insecure_host_key,
            workspace_root,
            max_concurrent_sessions,
            max_ws_payload_bytes,
            allowed_origins,
            allow_file_write,
            allow_oauth_relay,
        })
    }
}

fn env_parse<T: std::str::FromStr>(key: &str, default: T) -> Result<T>
where
    <T as std::str::FromStr>::Err: std::error::Error + Send + Sync + 'static,
{
    match env::var(key) {
        Ok(val) if !val.trim().is_empty() => val
            .trim()
            .parse::<T>()
            .with_context(|| format!("invalid value for {key}: {val}")),
        _ => Ok(default),
    }
}

fn env_bool(key: &str, default: bool) -> bool {
    match env::var(key) {
        Ok(val) => matches!(val.trim().to_ascii_lowercase().as_str(), "1" | "true" | "yes"),
        Err(_) => default,
    }
}
