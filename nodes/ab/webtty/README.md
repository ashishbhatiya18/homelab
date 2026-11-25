# webtty (Canary at `term2.ab18.in`)

A `FROM scratch` Rust WebTTY workspace (`axum` + `russh` + `russh-sftp` + embedded `@xterm/xterm` / `diff2html` UI) that connects over SSH to a restricted `webtty` user on node `ab`.

## Features
- **Multi-tab PTY sessions**: `Claude`, `agy`, and `Shell` tabs with 256 KB ANSI scrollback replay across disconnects and optional `tmux` persistence.
- **File Explorer**: SFTP-backed directory tree and syntax-highlighted preview scoped strictly to `WORKSPACE_ROOT` (canonicalized, symlink-escape blocked, read-only by default).
- **Git Diff Viewer**: Repo status, unstaged/staged/HEAD diffs, and recent commit diffs rendered with `diff2html`.
- **OAuth Callback Relay**: Paste any `http://localhost:<port>/callback?...` URL from your local browser into the **OAuth Relay** modal to deliver it to `127.0.0.1:<port>` on `ab`.

## Host Setup on Node `ab` (`dietpi-l`)

1. Run the host setup script on `ab`:
   ```sh
   sudo bash scripts/setup-host.sh
   ```
2. Copy the printed `SHA256:...` host fingerprint into `SSH_HOST_FINGERPRINT` in `compose.yaml`.
3. Install `claude` and `agy` for the `webtty` user (or globally in `/usr/local/bin`), and ensure `/home/dietpi/localstack/secrets/webtty/ssh_key` is readable by uid `1000`.
4. Ensure host firewall (`ufw`/`nftables`) allows TCP `22` from `11.11.11.0/24` (`internal_bridge` subnet) to `11.11.11.1`.
5. Push to `main` (builds `ghcr.io/ashishbhatiya18/webtty:latest`), apply Cloudflare ingress with `home cloud apply`, and deploy with `home deploy dietpi-l`.
