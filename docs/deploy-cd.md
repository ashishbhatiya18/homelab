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

Each node runs from its **bundle** (`ghcr.io/ashishbhatiya18/node-cd`), published by
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
  - name: dietpi
    ssh: dietpi@10.10.10.12
    stacks_dir: /home/dietpi/localstack/stacks
    bundle: ghcr.io/ashishbhatiya18/node-cd
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

# Watchtower HTTP API token (metrics at watchtower.citrusdental.in)
printf 'WATCHTOWER_HTTP_API_TOKEN=%s\n' "$(openssl rand -hex 32)" > secrets/cd-watchtower.env
```

### 3d — Set permissions

```sh
chmod 600 secrets/cloudflare_tunnel_token \
          secrets/ts_auth_key \
          secrets/cf_dns_api_token \
          secrets/pihole_web_password \
          secrets/cd-localstack.env \
          secrets/cd-watchtower.env \
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
```

---

## Phase 4 — Networks

Networks are declared in `nodes/cd/node.conf` and created by `node.sh start` (and so by
`home deploy`) before any stack starts.

> **Note:** The macvlan uses `parent=eth0`. If your LAN interface is named differently
> (check with `ip link`), change it in `nodes/cd/node.conf`.

---

## Phase 5 — Create Traefik acme.json

Traefik requires this file to exist with strict permissions before it starts. It is node
state, so it lives in `data/`, outside the repo checkout.

```sh
mkdir -p /home/dietpi/localstack/data/traefik
touch /home/dietpi/localstack/data/traefik/acme.json
chmod 600 /home/dietpi/localstack/data/traefik/acme.json
```

Seed Pi-hole's state directory from the repo (Pi-hole owns it from then on):

```sh
# after the first deploy has synced nodes/cd (or from the extracted release):
cp -a /home/dietpi/localstack/stacks/network/config/pihole /home/dietpi/localstack/data/pihole
```

---

## Phase 6 — First deploy (from your Mac)

```sh
home deploy dietpi --dry-run   # what will start, in node.conf order
home deploy dietpi
```

This creates the node's networks (from `nodes/cd/node.conf`), syncs the bundle into
`/home/dietpi/localstack/stacks/` and starts every stack in start order, waiting for
each to be healthy. After that, the `home` background job deploys new bundles by itself;
`home deploy dietpi --rollback` returns to the previous release.

On the node, `~/localstack/stacks/node.sh start|stop|restart|status [stack…]` runs the
same lifecycle by hand.

---

**Pi-hole note:** Pi-hole rewrites `pihole.toml` (password hash, settings) at runtime. That
happens in `data/pihole/`, so a deploy never touches it.

---

## Phase 7 — Verify all containers

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

## Phase 8 — GitOps workflow

From this point no manual deployments are needed.

```
Edit compose or config in repo  →  git commit  →  git push main
  └─► GitHub Actions: validate every compose file of the node, publish node-cd bundle
        └─► home deploy job (every 15 min, from your Mac): sync changed files,
            start changed stacks in order, health check, rollback on failure
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
| `home deploy` cannot pull the bundle | No GHCR login on the node | `docker login ghcr.io` (read:packages token) |
| `network internal_bridge not found` | Stack started without node.sh | `~/localstack/stacks/node.sh start <stack>` (creates networks first) |
| Traefik fails to start | `data/traefik/acme.json` missing or wrong permissions | `touch /home/dietpi/localstack/data/traefik/acme.json && chmod 600 $_` |
| macvlan creation fails | Wrong parent interface name | `ip link` to find correct name, edit `node.conf` |
| `api` or `peripheral` won't start | Missing secret file | Check `secrets/api/env`, `secrets/peripheral/env` and credential files exist |
| A push is not deployed | Bundle build failed validation, or the Mac was asleep | Check the *Node bundles* run in Actions; `home jobs` / `home deploy dietpi` |
