#!/usr/bin/env python3
"""Container hardening audit: every service in the given compose files must
drop all capabilities, set no-new-privileges, run as an explicit non-root
user with a read-only root filesystem, and stay off the host (no privileged
mode, host network, Docker socket or host /). An exception is a label on the
service that says why:

    hardening.root=<why>         runs as root (user unset, root or 0)
    hardening.host-access=<why>  privileged, host network, docker.sock or /
    hardening.caps=<why>         does not drop all capabilities
    hardening.writable=<why>     writable root filesystem

    scripts/hardening-audit.py nodes/*/*/compose.yaml

Missing env files (secrets live on the nodes) are stubbed per run.
Exit status: number of failing services (capped at 100).
"""
import json
import os
import re
import subprocess
import sys
import tempfile

ENV_FILE = re.compile(r"(/home/dietpi/localstack/secrets/[A-Za-z0-9_./-]+)")


def config(path, stub):
    """The resolved compose config of one file, env files stubbed."""
    text = open(path).read()
    # Paths only an env_file line names; secrets: files must exist for config too.
    text = re.sub(r"(env_file:\s*)" + ENV_FILE.pattern, r"\1" + stub, text)
    text = re.sub(r"(\n\s*-\s*)" + ENV_FILE.pattern + r"(\.env|/env)\b", r"\1" + stub, text)
    copy = os.path.join(os.path.dirname(path), ".hardening-audit.yaml")
    with open(copy, "w") as f:
        f.write(text)
    try:
        out = subprocess.run(["docker", "compose", "-f", copy, "config", "--format", "json"],
                             capture_output=True, text=True)
    finally:
        os.remove(copy)
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip().splitlines()[-1] if out.stderr else "compose config failed")
    return json.loads(out.stdout)


def audit(name, svc):
    """The failures of one service; empty when it passes."""
    labels = svc.get("labels") or {}
    if isinstance(labels, list):
        labels = dict(l.split("=", 1) if "=" in l else (l, "") for l in labels)
    waived = lambda k: bool(labels.get(f"hardening.{k}", "").strip())
    fail = []

    if "ALL" not in [c.upper().removeprefix("CAP_") for c in svc.get("cap_drop") or []] and not waived("caps"):
        fail.append("does not drop all capabilities")
    if not any(o.replace(" ", "") in ("no-new-privileges:true", "no-new-privileges")
               for o in svc.get("security_opt") or []):
        fail.append("no no-new-privileges")
    user = str(svc.get("user") or "").split(":")[0]
    if user in ("", "root", "0") and not waived("root"):
        fail.append("runs as root" if user else "no explicit user (image default)")

    host = []
    if svc.get("privileged"):
        host.append("privileged")
    if svc.get("network_mode") == "host":
        host.append("host network")
    for v in svc.get("volumes") or []:
        src = v.get("source", "") if isinstance(v, dict) else str(v).split(":")[0]
        if src == "/var/run/docker.sock":
            host.append("docker.sock")
        elif src == "/":
            host.append("host /")
    if host and not waived("host-access"):
        fail.append("host access: " + ", ".join(host))

    if not svc.get("read_only") and not waived("writable"):
        fail.append("writable root filesystem")
    return fail


def main(paths):
    failing = 0
    with tempfile.NamedTemporaryFile(suffix=".env") as stub:
        for path in sorted(paths):
            try:
                services = config(path, stub.name).get("services", {})
            except RuntimeError as e:
                print(f"✗ {path}: {e}")
                failing += 1
                continue
            for name, svc in sorted(services.items()):
                fail = audit(name, svc)
                where = f"{path.split('/')[1]}/{path.split('/')[2]}/{name}" if path.startswith("nodes/") else f"{path}:{name}"
                if fail:
                    failing += 1
                    print(f"✗ {where}: " + "; ".join(fail))
                else:
                    print(f"✓ {where}")
    print(f"\n{failing} failing service(s)")
    return min(failing, 100)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
