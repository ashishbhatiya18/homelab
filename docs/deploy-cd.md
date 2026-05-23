# CD Node — Deployment Guide
**Node:** 10.10.10.12 · Raspberry Pi 4 (ARM) · user: dietpi

## Stacks
| Stack | Services |
|---|---|
| `network` | traefik, tailscale, pihole |
| `localstack` | dockerproxy, watchtower, arcane |
| `citrusdental` | api, peripheral, cloudflared |

---

## Phase 0 — Prerequisites

SSH into the node and verify everything is present.

```sh
ssh dietpi@10.10.10.12

docker version           # Docker Engine
docker compose version   # Compose plugin (not standalone)
git --version
ls /dev/net/tun          # required by Tailscale
```

Install Docker if missing:
```sh
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker dietpi
# log out and back in
```

---

## Phase 1 — Git access (deploy key)

Generate a read-only deploy key on the node:
```sh
ssh-keygen -t ed25519 -C "cd-node-deploy" -f ~/.ssh/deploy_key -N ""
cat ~/.ssh/deploy_key.pub   # copy this
```

Add the public key to the repo:
**GitHub → repo → Settings → Deploy keys → Add deploy key**
- Title: `cd-node`
- Key: paste output above
- Allow write access: **no**

Configure SSH to use the key:
```sh
cat >> ~/.ssh/config <<'EOF'

Host github.com
  IdentityFile ~/.ssh/deploy_key
  IdentitiesOnly yes
EOF
chmod 600 ~/.ssh/config
```

Test:
```sh
ssh -T git@github.com
# Hi YOUR_USER/localstack! You've successfully authenticated...
```

---

## Phase 2 — Clone the repo

```sh
git clone git@github.com:YOUR_USER/localstack.git /home/dietpi/localstack/repo
```

---

## Phase 3 — Create secrets

All secrets live in `/home/dietpi/localstack/secrets/`. This directory is never committed to git.

```sh
mkdir -p /home/dietpi/localstack/secrets
chmod 700 /home/dietpi/localstack/secrets
```

### 3a — Network stack secrets

```sh
# Tailscale auth key — generate at https://login.tailscale.com/admin/settings/keys
echo "tskey-auth-XXXX" > secrets/ts_auth_key

# Cloudflare DNS API token — used by Traefik for ACME DNS-01 challenge
# Needs Zone:Read and DNS:Edit permissions
echo "your-cf-dns-api-token" > secrets/cf_dns_api_token

# Pi-hole web UI password (plaintext — pihole hashes it on first start)
echo "your-pihole-password" > secrets/pihole_web_password
```

### 3b — Citrusdental stack secrets

```sh
# Cloudflare tunnel token — from Zero Trust → Networks → Tunnels → citrusdental.in → Configure → Run token
echo "eyJhXXXX..." > secrets/cloudflare_tunnel_token

# api service env vars
mkdir -p secrets/api
cat > secrets/api/env <<'EOF'
DATABASE_URL=postgresql://...
# add all vars the api container needs
EOF

# peripheral service env vars + credential files
mkdir -p secrets/peripheral/gauth
mkdir -p secrets/peripheral/slack

cat > secrets/peripheral/env <<'EOF'
# add all vars the peripheral container needs
EOF

# Google auth credentials (from Google Cloud Console)
cp /path/to/credentials.json secrets/peripheral/gauth/credentials.json
cp /path/to/token.json       secrets/peripheral/gauth/token.json

# Slack bot token
echo "xoxb-..." > secrets/peripheral/slack/token.txt
```

### 3c — Localstack stack secrets

```sh
# Arcane env vars (add any required vars, can be empty)
touch secrets/cd-localstack.env
```

### 3d — Set permissions

```sh
chmod 600 secrets/cloudflare_tunnel_token \
          secrets/ts_auth_key \
          secrets/cf_dns_api_token \
          secrets/pihole_web_password \
          secrets/cd-localstack.env \
          secrets/api/env \
          secrets/peripheral/env \
          secrets/peripheral/gauth/credentials.json \
          secrets/peripheral/gauth/token.json \
          secrets/peripheral/slack/token.txt
```

The full secrets layout:
```
/home/dietpi/localstack/secrets/
  ts_auth_key
  cf_dns_api_token
  pihole_web_password
  cloudflare_tunnel_token
  cd-localstack.env
  api/
    env
  peripheral/
    env
    gauth/
      credentials.json
      token.json
    slack/
      token.txt
  gitops-agent.env          ← created automatically by install-agent.sh
```

---

## Phase 4 — Bootstrap Docker networks

Networks are pre-created outside of any compose stack so startup order never matters.

```sh
bash /home/dietpi/localstack/repo/nodes/cd/networks.sh
```

Verify both are present:
```sh
docker network ls | grep -E "internal_bridge|pihole_macvlan"
```

> **Note:** The macvlan uses `parent=eth0`. If your LAN interface is named differently
> (check with `ip link`), edit `nodes/cd/networks.sh` before running.

---

## Phase 5 — Create Traefik acme.json

Traefik requires this file to exist with strict permissions before it starts. It is gitignored.

```sh
touch /home/dietpi/localstack/repo/nodes/cd/network/config/traefik/acme.json
chmod 600 /home/dietpi/localstack/repo/nodes/cd/network/config/traefik/acme.json
```

---

## Phase 6 — Deploy the network stack first

The network stack (Traefik + Tailscale + Pi-hole) must be up before other stacks can be
routed externally. Deploy it once manually before the gitops agent takes over.

```sh
docker compose \
  -f /home/dietpi/localstack/repo/nodes/cd/network/compose.yaml \
  up -d

docker compose \
  -f /home/dietpi/localstack/repo/nodes/cd/network/compose.yaml \
  ps
```

**Pi-hole first-start note:** Pi-hole writes a password hash into `config/pihole/pihole.toml`
on first start. To prevent the gitops agent from overwriting this on every sync:

```sh
git -C /home/dietpi/localstack/repo \
  update-index --skip-worktree nodes/cd/network/config/pihole/pihole.toml
```

Check Traefik is up and connected to Cloudflare DNS for ACME:
```sh
docker logs traefik 2>&1 | grep -i "acme\|certificate\|error" | tail -20
```

---

## Phase 7 — Install the gitops agent

The install script clones the repo (or pulls if already cloned), writes the env file,
installs two systemd units, and starts the agent.

```sh
bash /home/dietpi/localstack/repo/scripts/install-agent.sh \
  git@github.com:YOUR_USER/localstack.git \
  cd
```

Two units are installed:
- `docker-networks.service` — runs `nodes/cd/networks.sh` on every boot before anything else
- `gitops-agent.service` — polls git every 60 s, deploys changed stacks automatically

Verify both are active:
```sh
systemctl status docker-networks.service
systemctl status gitops-agent.service
journalctl -u gitops-agent -f
```

The agent does a full deploy of all stacks on first start. Watch for `DEPLOY` lines — one
per stack.

---

## Phase 8 — Verify all containers

```sh
docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Image}}"
```

Expected:
```
NAMES             STATUS    IMAGE
traefik           Up        traefik:3.6.10
tailscale         Up        tailscale/tailscale:latest
pihole            Up        pihole/pihole
cloudflare        Up        cloudflare/cloudflared:latest
api               Up        ghcr.io/ashishbhatiya18/citrusdental-admin-api:rewrite
peripheral        Up        ghcr.io/ashishbhatiya18/citrusdental-admin-peripheral:rewrite
dockerproxy       Up        ghcr.io/tecnativa/docker-socket-proxy:latest
watchtower        Up        nickfedor/watchtower
arcane            Up        ghcr.io/getarcaneapp/arcane-headless:latest
```

Spot checks:
```sh
# Tailscale — node should appear in your admin console
docker exec tailscale tailscale status

# Pi-hole DNS resolving
docker exec pihole nslookup google.com 127.0.0.1

# Traefik routing table
docker exec traefik traefik version
```

---

## Phase 9 — GitOps workflow

From this point no manual deployments are needed.

```
Edit compose or config in repo  →  git commit  →  git push main
  └─► gitops-agent detects new commit within 60 s
        └─► docker compose up -d  (changed stacks only)
```

For Cloudflare DNS / cache rules:
```
Edit terraform/cloudflare.tf  →  git push main
  └─► GitHub Actions: terraform plan + apply (automatic)
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `Permission denied (publickey)` on clone | Deploy key not added to repo | Add `~/.ssh/deploy_key.pub` to repo Deploy Keys |
| `network internal_bridge not found` | networks.sh not run | `bash nodes/cd/networks.sh` |
| Traefik fails to start | `acme.json` missing or wrong permissions | `touch ... acme.json && chmod 600 ...` |
| Pi-hole keeps losing password | pwhash overwritten by git reset | `git update-index --skip-worktree nodes/cd/network/config/pihole/pihole.toml` |
| macvlan creation fails | Wrong parent interface name | `ip link` to find correct name, edit `networks.sh` |
| `api` or `peripheral` won't start | Missing secret file | Check `secrets/api/env`, `secrets/peripheral/env` and credential files exist |
| Agent not deploying a stack | Syntax error in compose | Run `docker compose -f nodes/cd/<stack>/compose.yaml config` to validate |
