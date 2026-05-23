#!/usr/bin/env python3
"""Create empty stub files for all env_file paths referenced in compose files.

Used by CI to let `docker compose config` validate syntax without real secrets.
Usage: stub_envfiles.py <compose.yaml> [<compose.yaml> ...]
"""
import os
import sys

import yaml

for compose_path in sys.argv[1:]:
    try:
        data = yaml.safe_load(open(compose_path))
    except Exception:
        continue
    for svc in (data or {}).get("services", {}).values():
        ef = svc.get("env_file", [])
        if isinstance(ef, str):
            ef = [ef]
        for e in ef:
            path = e.get("path", e) if isinstance(e, dict) else e
            if path.startswith("/"):
                os.makedirs(os.path.dirname(path), exist_ok=True)
                open(path, "a").close()
