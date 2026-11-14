#!/bin/sh
# Entrypoint for the official image, running as dietpi (1000:1000) with
# HOME=/config: installs Claude Code into ~/.local on a fresh config, then runs
# code-server with the paths and flags the linuxserver image used.
set -e
if [ ! -x "$HOME/.local/bin/claude" ]; then
  echo "[start] installing claude-code via install.sh"
  curl -fsSL https://claude.ai/install.sh | bash
fi
# Authelia (the `auth` middleware) is the login, so code-server has none.
exec dumb-init /usr/bin/code-server \
  --bind-addr 0.0.0.0:8443 \
  --user-data-dir /config/data \
  --extensions-dir /config/extensions \
  --disable-telemetry \
  --auth none \
  /repos
