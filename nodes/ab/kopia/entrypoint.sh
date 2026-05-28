#!/bin/sh

CONFIG="${KOPIA_CONFIG_PATH:-/app/config/repository.config}"

patch_enable_actions() {
  [ -f "$CONFIG" ] || return 0
  awk '{gsub(/"enableActions": false/, "\"enableActions\": true")}1' \
    "$CONFIG" > "$CONFIG.tmp" && mv "$CONFIG.tmp" "$CONFIG"
}

apply_policy() {
  echo "[entrypoint] Applying snapshot policy..."
  /usr/bin/kopia policy set \
    --before-folder-action=/scripts/pre-snapshot.sh \
    --scheduling-cron="${KOPIA_SNAPSHOT_CRON:-0 2 * * *}" \
    --add-ignore="data/kopia/cache" \
    --add-ignore="data/kopia/logs" \
    --add-ignore="data/kopia/tmp" \
    --add-ignore="data/kopia/repository" \
    --add-ignore="data/pg_data" \
    --add-ignore="data/redis_data" \
    --add-ignore="data/ollama_models/models/blobs" \
    --add-ignore="data/ollama_models/cache" \
    --add-ignore="data/jellyfin_data/cache" \
    --add-ignore="data/jellyfin_data/config/cache" \
    --add-ignore="data/jellyfin_data/config/data/transcodes" \
    --add-ignore="data/jellyfin_data/config/log" \
    --add-ignore="repo/.git" \
    /data-ab
}

init_repository() {
  echo "[entrypoint] Connecting to existing repository..."
  if /usr/bin/kopia repository connect rclone \
      --remote-path="${KOPIA_RCLONE_PATH}" \
      --password="${KOPIA_PASSWORD}" 2>/dev/null; then
    patch_enable_actions
    apply_policy
    return 0
  fi

  echo "[entrypoint] No existing repository found, creating..."
  if /usr/bin/kopia repository create rclone \
      --remote-path="${KOPIA_RCLONE_PATH}" \
      --password="${KOPIA_PASSWORD}"; then
    patch_enable_actions
    apply_policy
  else
    echo "[entrypoint] ERROR: repository init failed, server will start without a repository"
  fi
}

patch_enable_actions

if ! /usr/bin/kopia repository status > /dev/null 2>&1; then
  init_repository
fi

exec /usr/bin/kopia "$@"
