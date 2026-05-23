#!/usr/bin/env bash
# Bootstrap the GitOps agent on a fresh node.
# Usage: bash install-agent.sh <git-repo-url> <node>
#   git-repo-url   e.g. git@github.com:youruser/localstack.git
#   node           ab (for 10.10.10.11) or cd (for 10.10.10.12)
#
# Prerequisites on the node:
#   - git, docker, docker-compose-plugin installed
#   - SSH deploy key added to the git host (or use HTTPS with a token)
#   - Run as dietpi (sudo access needed for systemd steps)
set -euo pipefail

REPO_URL="${1:?Usage: install-agent.sh <repo-url> <node>}"
NODE="${2:?Usage: install-agent.sh <repo-url> <node>}"
REPO_DIR="/home/dietpi/localstack/repo"
SECRETS_DIR="/home/dietpi/localstack/secrets"

log() { echo "[install] $*"; }

# ── 1. Clone or update the repo ──────────────────────────────────────────────
if [[ -d "$REPO_DIR/.git" ]]; then
  log "Repo already exists at $REPO_DIR, pulling latest"
  git -C "$REPO_DIR" pull --ff-only
else
  log "Cloning $REPO_URL → $REPO_DIR"
  git clone "$REPO_URL" "$REPO_DIR"
fi

# ── 2. Bootstrap networks before anything else starts ───────────────────────
log "Bootstrapping networks for node $NODE"
bash "$REPO_DIR/nodes/$NODE/networks.sh"

# ── 3. Write the env file consumed by the systemd unit ───────────────────────
mkdir -p "$SECRETS_DIR"
ENV_FILE="$SECRETS_DIR/gitops-agent.env"

if [[ -f "$ENV_FILE" ]]; then
  log "Env file already exists at $ENV_FILE — skipping (edit manually if needed)"
else
  cat > "$ENV_FILE" <<EOF
GITOPS_NODE=$NODE
GITOPS_REPO_DIR=$REPO_DIR
GITOPS_POLL_INTERVAL=60
GITOPS_BRANCH=main
EOF
  log "Wrote $ENV_FILE"
fi

# ── 4. Install and enable systemd services ───────────────────────────────────
log "Installing systemd units"
sudo cp "$REPO_DIR/scripts/docker-networks.service" /etc/systemd/system/docker-networks.service
sudo cp "$REPO_DIR/scripts/gitops-agent.service"    /etc/systemd/system/gitops-agent.service
sudo systemctl daemon-reload
# docker-networks runs once at boot before the agent; enable but don't start
# manually — it will be pulled in automatically by gitops-agent.service via Before=
sudo systemctl enable docker-networks.service
sudo systemctl enable gitops-agent.service
sudo systemctl restart gitops-agent.service

log "Done. Monitor with: journalctl -u gitops-agent -f"
