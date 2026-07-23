#!/usr/bin/env bash
set -e

export HOME=/config

if [ ! -x "${HOME}/.local/bin/claude" ]; then
  echo "[init-claude-code] installing claude-code via install.sh"
  curl -fsSL https://claude.ai/install.sh | bash
  chown -R abc:abc "${HOME}/.local"
else
  echo "[init-claude-code] claude-code already installed, skipping"
fi

ln -sf "${HOME}/.local/bin/claude" /usr/local/bin/claude
