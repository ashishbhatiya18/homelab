#!/usr/bin/env bash
# Network bootstrap for node ab (10.10.10.11).
# Idempotent — safe to run on every agent startup and Docker daemon restart.
# Networks managed here are external to all compose stacks so startup order
# of individual stacks does not matter.
set -euo pipefail

docker network inspect tailscale_bridge >/dev/null 2>&1 || \
  docker network create \
    --driver bridge \
    --subnet 11.11.11.0/24 \
    --gateway 11.11.11.1 \
    tailscale_bridge

echo "[networks] tailscale_bridge ready"

docker network inspect data-layer >/dev/null 2>&1 || \
  docker network create data-layer

echo "[networks] data-layer ready"
