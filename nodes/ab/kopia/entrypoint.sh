#!/bin/sh
CONFIG=/app/config/repository.config
if [ -f "$CONFIG" ]; then
  awk '{gsub(/"enableActions": false/, "\"enableActions\": true")}1' "$CONFIG" > "$CONFIG.tmp" && mv "$CONFIG.tmp" "$CONFIG"
fi
exec /usr/bin/kopia "$@"
