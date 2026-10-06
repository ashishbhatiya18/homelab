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
rsync --version | head -1
ls /dev/net/tun          # required by Tailscale
```

Install Docker if missing:
```sh
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker dietpi
# log out and back in
```

---

## Phase 1 — Registry access

Each node runs from its **bundle** (`ghcr.io/ashishbhatiya18/node-ab`), published by
`.github/workflows/node-bundles.yml` on every push to `main` that touches `nodes/`.
There is no git checkout and no agent on the node: `home` (on your Mac) deploys over
SSH. The node only needs a read-only GHCR login for private images:

```sh
docker login ghcr.io -u ashishbhatiya18   # token with read:packages
```

---

## Phase 2 — Add the node to `home` (on your Mac)

In `~/.config/home/config.yaml`:

```yaml
nodes:
  - name: dietpi-l
    ssh: dietpi@10.10.10.11
    stacks_dir: /home/dietpi/localstack/stacks
    bundle: ghcr.io/ashishbhatiya18/node-ab
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
KOPIA_PASSWORD=your-kopia-repository-password
KOPIA_SERVER_USERNAME=admin
KOPIA_SERVER_PASSWORD=admin
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
```

---

## Phase 4 — Fill in OAuth2-proxy secrets

The `config.toml` in the repo is a template with placeholder values. Copy it into the
secrets directory and fill in the real values there (the repo checkout stays untouched):

```sh
mkdir -p /home/dietpi/localstack/secrets/oauth2-proxy
cp /home/dietpi/localstack/stacks/network/config/oauth2-proxy/config.toml \
   /home/dietpi/localstack/secrets/oauth2-proxy/config.toml
chmod 600 /home/dietpi/localstack/secrets/oauth2-proxy/config.toml
nano /home/dietpi/localstack/secrets/oauth2-proxy/config.toml
# Replace:
#   client_id    = "REPLACE_WITH_GOOGLE_CLIENT_ID"
#   client_secret = "REPLACE_WITH_GOOGLE_CLIENT_SECRET"
#   cookie_secret = "REPLACE_WITH_COOKIE_SECRET_16_24_OR_32_BYTES"
#
# Generate a cookie secret:
#   openssl rand -base64 24
```

---

## Phase 5 — Create Traefik acme.json

Traefik requires this file with strict permissions. It is node state, so it lives in
`data/`, outside the repo checkout.

```sh
mkdir -p /home/dietpi/localstack/data/traefik
touch /home/dietpi/localstack/data/traefik/acme.json
chmod 600 /home/dietpi/localstack/data/traefik/acme.json
```

---

## Phase 7 — First deploy (from your Mac)

```sh
home deploy dietpi-l --dry-run   # what will start, in node.conf order
home deploy dietpi-l
```

This creates the node's networks (from `nodes/ab/node.conf`), syncs the bundle into
`/home/dietpi/localstack/stacks/` and starts every stack in start order, waiting for
each to be healthy. After that, the `home` background job deploys new bundles by itself;
`home deploy dietpi-l --rollback` returns to the previous release.

On the node, `~/localstack/stacks/node.sh start|stop|restart|status [stack…]` runs the
same lifecycle by hand.

---

## Phase 8 — Verify all containers

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
| `home deploy` cannot pull the bundle | No GHCR login on the node | `docker login ghcr.io` (read:packages token) |
| `network internal_bridge not found` | Stack started without node.sh | `~/localstack/stacks/node.sh start <stack>` (creates networks first) |
| Traefik fails to start | `data/traefik/acme.json` missing or wrong permissions | `touch /home/dietpi/localstack/data/traefik/acme.json && chmod 600 $_` |
| cloudflared `tunnel not found` | Wrong `TUNNEL_TOKEN` in `ab-cloudflared.env` | Re-fetch with `cloudflared tunnel token ab18-localstack` and restart cloudflared |
| oauth2-proxy redirect loop | Placeholder secrets in config.toml | Fill in real values in `secrets/oauth2-proxy/config.toml` |
| Immich fails to connect to DB | `DB_PASSWORD` mismatch | Must match `POSTGRES_PASSWORD` in `ab-data.env` |
| Vaultwarden fails to connect to DB | `DATABASE_URL` password mismatch | Must match password used in `CREATE ROLE vaultwarden` |
| ESPHome config missing | `/home/dietpi/localstack/data/esphome/` not restored | Restore from backup (kopia) |
| Syncthing won't start | `/home/dietpi/localstack/data/syncthing/` not present | Restore from backup (kopia); an empty dir is seeded from `config.xml.template` |
| Agent not deploying a stack | Syntax error in compose | `docker compose -f nodes/ab/<stack>/compose.yaml config` |
