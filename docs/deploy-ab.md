# AB Node — Deployment Guide
**Node:** 10.10.10.11 · amd64 · user: dietpi

## Stacks
| Stack | Services | Secrets needed |
|---|---|---|
| `network` | tailscale, traefik, whoami, cloudflared, oauth2-proxy | `ts_auth_key`, `cf_dns_api_token`, `cloudflared-credentials.json` |
| `data` | postgres, redis, mariadb | `ab-data.env` |
| `immich` | immich-server, machine-learning | `ab-immich.env` |
| `kopia` | kopia | `ab-kopia.env` |
| `localstack` | arcane, watchtower | `ab-localstack.env` |
| `vaultwarden` | vaultwarden | — |
| `homeautomation` | esphome | — (config gitignored) |
| `syncthing` | syncthing | — (config gitignored) |
| `homepage` | dockerproxy, homepage | — |
| `filebrowser` | filebrowser | — |
| `media` | jellyfin | — |
| `bentopdf` | stirling-pdf | — |
| `dockerhub` | registry | — |
| `excalidraw` | excalidraw | — |
| `rustpad` | rustpad | — |

---

## Phase 0 — Prerequisites

```sh
ssh dietpi@10.10.10.11

docker version
docker compose version
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

```sh
ssh-keygen -t ed25519 -C "ab-node-deploy" -f ~/.ssh/deploy_key -N ""
cat ~/.ssh/deploy_key.pub   # copy this
```

Add to GitHub: **repo → Settings → Deploy keys → Add deploy key**
- Title: `ab-node`
- Key: paste above
- Allow write access: **no**

```sh
cat >> ~/.ssh/config <<'EOF'

Host github.com
  IdentityFile ~/.ssh/deploy_key
  IdentitiesOnly yes
EOF
chmod 600 ~/.ssh/config

# Verify
ssh -T git@github.com
```

---

## Phase 2 — Clone the repo

```sh
git clone git@github.com:YOUR_USER/localstack.git /home/dietpi/localstack/repo
```

---

## Phase 3 — Create secrets

```sh
mkdir -p /home/dietpi/localstack/secrets
chmod 700 /home/dietpi/localstack/secrets
cd /home/dietpi/localstack/secrets
```

### 3a — Network stack

```sh
# Tailscale auth key — https://login.tailscale.com/admin/settings/keys
echo "tskey-auth-XXXX" > ts_auth_key

# Cloudflare DNS API token — Zone:Read + DNS:Edit permissions
echo "your-cf-dns-api-token" > cf_dns_api_token

# Cloudflare tunnel credentials JSON — from cloudflared on the node
# Run once to create the tunnel (skip if tunnel already exists):
#   docker run --rm cloudflare/cloudflared tunnel login
#   docker run --rm cloudflare/cloudflared tunnel create ab18-localstack
# Then copy the credentials file:
cp ~/.cloudflared/<tunnel-id>.json cloudflared-credentials.json
```

Update the tunnel ID in the cloudflared config to match:
```sh
nano /home/dietpi/localstack/repo/nodes/ab/network/cloudflared/config.yml
# set: tunnel: <your-tunnel-id>
```

Mark it so git does not overwrite your edit:
```sh
git -C /home/dietpi/localstack/repo \
  update-index --skip-worktree nodes/ab/network/cloudflared/config.yml
```

### 3b — Data stack (postgres + mariadb)

```sh
cat > ab-data.env <<'EOF'
POSTGRES_USER=immich
POSTGRES_PASSWORD=your-pg-password
POSTGRES_DB=immich
MYSQL_ROOT_PASSWORD=your-mysql-root-password
MYSQL_DATABASE=your-db
MYSQL_USER=your-user
MYSQL_PASSWORD=your-mysql-password
EOF
```

### 3c — Immich stack

```sh
cat > ab-immich.env <<'EOF'
DB_HOSTNAME=postgres
DB_USERNAME=immich
DB_PASSWORD=your-pg-password
DB_DATABASE_NAME=immich
REDIS_HOSTNAME=redis
EOF
```

### 3d — Kopia stack

```sh
cat > ab-kopia.env <<'EOF'
KOPIA_PASSWORD=your-kopia-repository-password
# Add any other Kopia vars (S3 credentials, etc.)
EOF
```

### 3e — Localstack stack (Arcane)

```sh
touch ab-localstack.env   # add vars if Arcane needs any
```

### 3f — Set permissions

```sh
chmod 600 ts_auth_key cf_dns_api_token cloudflared-credentials.json \
          ab-data.env ab-immich.env ab-kopia.env ab-localstack.env
```

### Complete secrets layout

```
/home/dietpi/localstack/secrets/
  ts_auth_key
  cf_dns_api_token
  cloudflared-credentials.json
  ab-data.env
  ab-immich.env
  ab-kopia.env
  ab-localstack.env
  gitops-agent.env          ← created by install-agent.sh
```

---

## Phase 4 — Fill in OAuth2-proxy secrets

The `config.toml` is committed with placeholder values. Fill in the real values on the node
and mark the file so git does not overwrite them:

```sh
nano /home/dietpi/localstack/repo/nodes/ab/network/config/oauth2-proxy/config.toml
# Replace:
#   client_id    = "REPLACE_WITH_GOOGLE_CLIENT_ID"
#   client_secret = "REPLACE_WITH_GOOGLE_CLIENT_SECRET"
#   cookie_secret = "REPLACE_WITH_COOKIE_SECRET_16_24_OR_32_BYTES"
#
# Generate a cookie secret:
#   openssl rand -base64 24

git -C /home/dietpi/localstack/repo \
  update-index --skip-worktree \
  nodes/ab/network/config/oauth2-proxy/config.toml
```

---

## Phase 5 — Bootstrap Docker networks

```sh
bash /home/dietpi/localstack/repo/nodes/ab/networks.sh

# Verify
docker network ls | grep tailscale_bridge
```

---

## Phase 6 — Create Traefik acme.json

Traefik requires this file with strict permissions. It is gitignored.

```sh
touch /home/dietpi/localstack/repo/nodes/ab/network/config/traefik/acme.json
chmod 600 /home/dietpi/localstack/repo/nodes/ab/network/config/traefik/acme.json
```

---

## Phase 7 — Deploy the network stack first

```sh
docker compose \
  -f /home/dietpi/localstack/repo/nodes/ab/network/compose.yaml \
  up -d

docker compose \
  -f /home/dietpi/localstack/repo/nodes/ab/network/compose.yaml \
  ps
```

Verify Traefik is issuing certificates and cloudflared connects:
```sh
docker logs traefik    2>&1 | grep -i "acme\|certificate\|error" | tail -20
docker logs cloudflared 2>&1 | tail -20
```

---

## Phase 8 — Install the gitops agent

```sh
bash /home/dietpi/localstack/repo/scripts/install-agent.sh \
  git@github.com:YOUR_USER/localstack.git \
  ab
```

Monitor the first full deploy:
```sh
journalctl -u gitops-agent -f
```

---

## Phase 9 — Verify all containers

```sh
docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Image}}"
```

Expected running containers:
```
tailscale, traefik, whoami, cloudflared, oauth2-proxy
postgres, redis, mariadb
immich-server, immich-machine-learning
kopia
arcane, watchtower
vaultwarden
esphome
syncthing
dockerproxy, homepage
filebrowser
jellyfin
stirling-pdf
registry
excalidraw
rustpad
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `Permission denied (publickey)` on clone | Deploy key not added | Add `~/.ssh/deploy_key.pub` to repo Deploy Keys |
| `network tailscale_bridge not found` | networks.sh not run | `bash nodes/ab/networks.sh` |
| Traefik fails to start | `acme.json` missing or wrong permissions | `touch acme.json && chmod 600 acme.json` |
| cloudflared `tunnel not found` | Tunnel ID mismatch in config.yml | Update tunnel ID and re-run `docker compose up -d cloudflared` |
| oauth2-proxy redirect loop | Placeholder secrets in config.toml | Fill in real values, mark skip-worktree |
| Immich fails to connect to DB | Wrong `DB_PASSWORD` in `ab-immich.env` | Must match `POSTGRES_PASSWORD` in `ab-data.env` |
| ESPHome config missing | `homeautomation/config/esphome/` not restored | Restore from backup — this dir is gitignored |
| Syncthing won't start | `syncthing/config/` not present | Restore from backup — this dir is gitignored |
| Agent not deploying a stack | Syntax error in compose | `docker compose -f nodes/ab/<stack>/compose.yaml config` |
