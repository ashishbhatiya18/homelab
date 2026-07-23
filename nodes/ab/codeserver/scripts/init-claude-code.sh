#!/usr/bin/env bash
set -e

export NPM_CONFIG_PREFIX=/config/.npm-global

if [ ! -x "${NPM_CONFIG_PREFIX}/bin/claude" ]; then
  echo "[init-claude-code] installing @anthropic-ai/claude-code into ${NPM_CONFIG_PREFIX}"
  mkdir -p "${NPM_CONFIG_PREFIX}"
  npm install -g @anthropic-ai/claude-code
  chown -R abc:abc "${NPM_CONFIG_PREFIX}"
else
  echo "[init-claude-code] claude-code already installed, skipping"
fi

ln -sf "${NPM_CONFIG_PREFIX}/bin/claude" /usr/local/bin/claude
