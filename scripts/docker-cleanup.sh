#!/usr/bin/env bash
# Prune unused Docker images, stopped containers, and dangling volumes.
# Designed to run as a systemd timer (daily). Safe to run alongside a live stack
# because --filter "until=24h" only removes images unused for at least 24 hours.
set -euo pipefail

log() { echo "[docker-cleanup] $(date -Iseconds) $*"; }

log "Starting Docker cleanup"

log "Pruning containers (stopped)"
docker container prune -f 2>&1 | sed 's/^/  /'

log "Pruning images unused for >24h"
docker image prune -a -f --filter "until=24h" 2>&1 | sed 's/^/  /'

log "Pruning dangling volumes"
docker volume prune -f 2>&1 | sed 's/^/  /'

log "Pruning unused networks"
docker network prune -f 2>&1 | sed 's/^/  /'

log "Cleanup complete"
