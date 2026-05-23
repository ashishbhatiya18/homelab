# AB Node — Deployment Guide
**Node:** 10.10.10.11 · amd64 · user: dietpi

## Stacks
| Stack | Services | Secrets needed |
|---|---|---|
| `network` | tailscale, traefik, whoami, cloudflared, oauth2-proxy | `ts_auth_key`, `cf_dns_api_token`, `ab-cloudflared.env` |
| `data` | postgres, redis | `ab-data.env` |
| `immich` | immich-server, immich_machine_learning | `ab-immich.env` |
| `kopia` | kopia | `ab-kopia.env` |
| `localstack` | watchtower | `ab-localstack.env` |
| `vaultwarden` | vaultwarden | `ab-vaultwarden.env` |
| `homeautomation` | esphome | `ab-homeautomation.env` (config gitignored) |
| `syncthing` | syncthing | `ab-syncthing.env` (config gitignored) |
| `filebrowser` | filebrowser | `ab-filebrowser.env` |
| `media` | jellyfin | `ab-media.env` |
| `bentopdf` | stirlingpdf | — |
| `excalidraw` | excalidraw | `ab-excalidraw.env` |
| `rustpad` | rustpad | `ab-rustpad.env` |

## Exposed services (ab18.in)

Tunnel name: `ab18-localstack` · Public ingress is **Terraform-managed** (`terraform/ab18.tf`).

**Public (Cloudflare tunnel → Traefik):**

| Subdomain | Service |
|---|---|
| `auth.ab18.in` | oauth2-proxy |
| `draw.ab18.in` | excalidraw |
| `pad.ab18.in` | rustpad |
| `pdf.ab18.in` | stirlingpdf |
| `photos.ab18.in` | immich-server |
| `vault.ab18.in` | vaultwarden (Cloudflare cache bypass active) |
| `whoami.ab18.in` | whoami |

**Internal only (Tailscale → Traefik, not in tunnel):**

| Subdomain | Service |
|---|---|
| `backup.ab18.in` | kopia |
| `esphome.ab18.in` | esphome |
| `files.ab18.in` | filebrowser |
| `jelly.ab18.in` | jellyfin |
| `proxy.ab18.in` | traefik dashboard |
| `sync.ab18.in` | syncthing |
| `pg.ab18.in` | postgres (TCP/TLS) |
| `redis.ab18.in` | redis (TCP/TLS) |

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

# Cloudflare tunnel token — get it from the dashboard or CLI:
#   cloudflared tunnel token ab18-localstack
# The tunnel and its ingress rules are managed by Terraform (terraform/ab18.tf).
cat > ab-cloudflared.env <<'EOF'
TUNNEL_TOKEN=your-tunnel-token
EOF
```

> **Note:** Tunnel ingress routing is fully managed in `terraform/ab18.tf` via
> `cloudflare_zero_trust_tunnel_cloudflared_config`. Do **not** edit routing rules
> locally — they are pushed from Terraform.

### 3b — Data stack (postgres + redis)

```sh
cat > ab-data.env <<'EOF'
DB_USERNAME=immich
DB_DATABASE_NAME=photos
POSTGRES_INITDB_ARGS=--data-checksums
POSTGRES_PASSWORD=your-pg-password
EOF
```

### 3b.1 — Provision Vaultwarden postgres user/database

Vaultwarden connects as `vaultwarden@pg.ab18.in:443/ab18`. After the data stack is running, create the role and database:

```sh
docker exec -it postgres psql -U immich -d postgres -c "
  CREATE ROLE vaultwarden WITH LOGIN PASSWORD 'your-vaultwarden-db-password';
  CREATE DATABASE ab18 OWNER vaultwarden;
"
```

### 3c — Immich stack

```sh
cat > ab-immich.env <<'EOF'
DB_HOSTNAME=postgres
DB_USERNAME=immich
DB_PASSWORD=your-pg-password
DB_DATABASE_NAME=photos
REDIS_HOSTNAME=redis
EOF
```

### 3d — Kopia stack

```sh
cat > ab-kopia.env <<'EOF'
KOPIA_DATADIR=/home/dietpi/localstack/data/kopia
KOPIA_PASSWORD=your-kopia-repository-password
EOF
```

### 3e — Localstack stack (watchtower)

```sh
cat > ab-localstack.env <<'EOF'
WATCHTOWER_UPDATE_ON_START=true
WATCHTOWER_POLL_INTERVAL=300
EOF
```

### 3f — Vaultwarden stack

```sh
cat > ab-vaultwarden.env <<'EOF'
TZ=Asia/Kolkata
DATABASE_URL=postgresql://vaultwarden:your-vaultwarden-db-password@pg.ab18.in:443/ab18
WEBSOCKET_ENABLED=true
DOMAIN=https://vault.ab18.in
SESSION_LIFETIME_SECONDS=2592000
EOF
```

### 3g — Media stack (jellyfin)

```sh
cat > ab-media.env <<'EOF'
PUID=1000
PGID=1000
TZ=Asia/Kolkata
UMASK_SET=022
EOF
```

### 3h — Filebrowser stack

```sh
cat > ab-filebrowser.env <<'EOF'
PUID=1000
PGID=1000
TZ=Asia/Kolkata
FB_AUTH_HEADER=X-Auth-Request-User
FB_AUTH_METHOD=proxy
EOF
```

### 3i — Syncthing stack

```sh
cat > ab-syncthing.env <<'EOF'
PUID=1000
PGID=1000
TZ=Asia/Kolkata
EOF
```

### 3j — Rustpad stack

```sh
cat > ab-rustpad.env <<'EOF'
EXPIRY_DAYS=30
SQLITE_URI=/data/db.sqlite
RUST_LOG=info
EOF
```

### 3k — Excalidraw stack

```sh
cat > ab-excalidraw.env <<'EOF'
NODE_ENV=production
EOF
```

### 3l — Homeautomation stack (esphome)

```sh
cat > ab-homeautomation.env <<'EOF'
ESPHOME_DASHBOARD_USE_PING=true
EOF
```

### 3m — Set permissions

```sh
chmod 600 ts_auth_key cf_dns_api_token ab-cloudflared.env \
          ab-data.env ab-immich.env ab-kopia.env ab-localstack.env \
          ab-vaultwarden.env ab-media.env ab-filebrowser.env \
          ab-syncthing.env ab-rustpad.env ab-excalidraw.env \
          ab-homeautomation.env
```

### Complete secrets layout

```
/home/dietpi/localstack/secrets/
  ts_auth_key
  cf_dns_api_token
  ab-cloudflared.env
  ab-data.env
  ab-immich.env
  ab-kopia.env
  ab-localstack.env
  ab-vaultwarden.env
  ab-media.env
  ab-filebrowser.env
  ab-syncthing.env
  ab-rustpad.env
  ab-excalidraw.env
  ab-homeautomation.env
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
docker network ls | grep -E "tailscale_bridge|data-layer"
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
postgres, redis
immich-server, immich_machine_learning
kopia
watchtower
vaultwarden
esphome
syncthing
filebrowser
jellyfin
stirlingpdf
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
| cloudflared `tunnel not found` | Wrong `TUNNEL_TOKEN` in `ab-cloudflared.env` | Re-fetch with `cloudflared tunnel token ab18-localstack` and restart cloudflared |
| oauth2-proxy redirect loop | Placeholder secrets in config.toml | Fill in real values, mark skip-worktree |
| Immich fails to connect to DB | `DB_PASSWORD` mismatch | Must match `POSTGRES_PASSWORD` in `ab-data.env` |
| Vaultwarden fails to connect to DB | `DATABASE_URL` password mismatch | Must match password used in `CREATE ROLE vaultwarden` |
| ESPHome config missing | `homeautomation/config/esphome/` not restored | Restore from backup — this dir is gitignored |
| Syncthing won't start | `syncthing/config/` not present | Restore from backup — this dir is gitignored |
| Agent not deploying a stack | Syntax error in compose | `docker compose -f nodes/ab/<stack>/compose.yaml config` |
