#!/usr/bin/env bash
# Network bootstrap for node cd (10.10.10.12).
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

# Macvlan for pihole — physical interface, survives daemon restarts.
# parent=eth0 must match the host's LAN interface name.
docker network inspect pihole_macvlan >/dev/null 2>&1 || \
  docker network create \
    --driver macvlan \
    --subnet 10.10.10.0/24 \
    --gateway 10.10.10.1 \
    --ip-range 10.10.10.10/32 \
    -o parent=eth0 \
    pihole_macvlan

echo "[networks] tailscale_bridge and pihole_macvlan ready"
