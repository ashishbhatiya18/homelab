#!/usr/bin/env bash
# GitOps agent — polls git and redeploys changed Docker Compose stacks.
# Runs as a systemd service on each node. Configuration via environment:
#   GITOPS_NODE        required  node name for this host (ab or cd)
#   GITOPS_REPO_DIR    optional  path to git repo (default: /home/dietpi/localstack/repo)
#   GITOPS_POLL_INTERVAL  optional  seconds between polls (default: 60)
#   GITOPS_REMOTE      optional  git remote name (default: origin)
#   GITOPS_BRANCH      optional  git branch to track (default: main)
set -euo pipefail

REPO_DIR="${GITOPS_REPO_DIR:-/home/dietpi/localstack/repo}"
POLL_INTERVAL="${GITOPS_POLL_INTERVAL:-60}"
NODE="${GITOPS_NODE:?GITOPS_NODE must be set (ab or cd)}"
REMOTE="${GITOPS_REMOTE:-origin}"
BRANCH="${GITOPS_BRANCH:-main}"
NODE_DIR="nodes/$NODE"

log() { echo "[gitops] $(date -Iseconds) $*"; }

ensure_networks() {
  local script="$REPO_DIR/nodes/$NODE/networks.sh"
  if [[ -f "$script" ]]; then
    log "Ensuring networks for node $NODE"
    bash "$script"
  else
    log "WARN: no networks.sh found at $script"
  fi
}

deploy_stack() {
  local rel_dir="$1"
  local compose_file="$REPO_DIR/$rel_dir/compose.yaml"
  [[ -f "$compose_file" ]] || { log "SKIP $rel_dir — no compose.yaml"; return 0; }
  log "DEPLOY $rel_dir"
  docker compose -f "$compose_file" up -d --remove-orphans 2>&1 | sed "s/^/  /"
}

deploy_all() {
  log "Full sync: deploying all $NODE_DIR stacks"
  while IFS= read -r -d '' compose_file; do
    local rel_dir
    rel_dir=$(dirname "${compose_file#"$REPO_DIR"/}")
    deploy_stack "$rel_dir" || log "ERROR in $rel_dir (continuing)"
  done < <(find "$REPO_DIR/$NODE_DIR" -name "compose.yaml" -print0 | sort -z)
}

main() {
  log "Starting — node=$NODE  repo=$REPO_DIR  interval=${POLL_INTERVAL}s"

  cd "$REPO_DIR"
  git fetch "$REMOTE" "$BRANCH" --quiet
  git checkout -B "$BRANCH" --track "$REMOTE/$BRANCH" --quiet 2>/dev/null || \
    git reset --hard "$REMOTE/$BRANCH" --quiet

  # Networks must exist before any stack starts — covers fresh boots and
  # Docker daemon restarts where containers come back before networks do.
  ensure_networks
  deploy_all

  local last_commit
  last_commit=$(git rev-parse HEAD)
  log "Tracking from $last_commit"

  while true; do
    sleep "$POLL_INTERVAL"

    git fetch "$REMOTE" "$BRANCH" --quiet 2>/dev/null || {
      log "WARN: git fetch failed, retrying next cycle"
      continue
    }

    local remote_commit
    remote_commit=$(git rev-parse "$REMOTE/$BRANCH")
    [[ "$last_commit" == "$remote_commit" ]] && continue

    log "New commits: $last_commit → $remote_commit"

    local changed_stacks
    changed_stacks=$(
      git diff --name-only "$last_commit" "$remote_commit" \
        | grep "^${NODE_DIR}/" \
        | cut -d/ -f1-3 \
        | sort -u
    ) || true

    git pull --ff-only "$REMOTE" "$BRANCH" --quiet
    last_commit=$(git rev-parse HEAD)

    if [[ -z "$changed_stacks" ]]; then
      log "No $NODE_DIR stack changes in this push, skipping redeploy"
      continue
    fi

    while IFS= read -r stack; do
      [[ -z "$stack" ]] && continue
      deploy_stack "$stack" || log "ERROR deploying $stack (continuing)"
    done <<< "$changed_stacks"

    log "Cycle complete at $last_commit"
  done
}

main
