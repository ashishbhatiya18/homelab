# AB Node — Deployment Guide
**Node:** 10.10.10.11 · amd64 · user: dietpi

## Stacks
| Stack | Services | Secrets needed |
|---|---|---|
| `network` | tailscale, traefik, whoami, cloudflared, authelia | `network/{ts_auth_key,cf_dns_api_token,cloudflared_tunnel_token}`, `network/authelia/` |
| `data` | postgres, redis | `data/postgres_password`, `data/redis.conf` |
| `immich` | immich-server, immich_machine_learning | `immich/{db_url,redis_password}` |
| `kopia` | kopia | `kopia/{repository_password,postgres_password}` |
| `localstack` | watchtower, heartbeat | `localstack.env`, `localstack/hc_ping_url` |
| `vaultwarden` | vaultwarden | `vaultwarden/{admin_token,database_url,authelia_client_secret}` |
| `memos` | memos | `memos.env`, `memos/{idp-authelia.json,admin_password}` |
| `treasuredash` | treasuredash | `treasuredash.env` |
| `syncthing` | syncthing | `syncthing/` (its identity; config gitignored) |
| `homeautomation`, `filebrowser`, `media`, `bentopdf`, `excalidraw`, `rustpad`, `codeserver`, `claudecode`, `isponsorblock`, `ntfy` | | — |

Paths are relative to `/home/dietpi/localstack/secrets/`; the layout and its
rules are in [secrets.md](secrets.md).

## Exposed services (ab18.in)

Tunnel name: `ab18-localstack` · Public ingress is declared in `cloud/tunnels/ab18-localstack.yaml` (`home cloud`).

**Public (Cloudflare tunnel → Traefik):**

| Subdomain | Service |
|---|---|
| `id.ab18.in` | authelia (passkey login, OIDC) |
| `draw.ab18.in` | excalidraw |
| `notes.ab18.in` | memos (sign-in via Authelia OIDC) |
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

Secrets follow [secrets.md](secrets.md): a secret file per value under
`secrets/<stack>/`, and `secrets/<stack>.env` only for apps that can't read a
file. Everything else is configuration in the stacks' compose files. Files are
mode 600 and owned by the user their container runs as (dietpi unless noted);
`home deploy` refuses a release whose stacks reference a secret file that is
missing, so create a stack's secrets before its first deploy.

```sh
cd /home/dietpi/localstack
install -d -m 700 secrets
cd secrets
umask 077
put() { mkdir -p "$(dirname "$1")"; printf '%s' "$2" > "$1"; }   # put <file> <value>
```

### 3a — Network stack

```sh
put network/ts_auth_key "tskey-auth-XXXX"            # https://login.tailscale.com/admin/settings/keys
put network/cf_dns_api_token "your-cf-dns-api-token" # Zone:Read + DNS:Edit
```

The tunnel token: from the Mac, `home tunnel token ab18-localstack --install`
writes it to `network/cloudflared_tunnel_token` (the `secret:` of
`cloud/tunnels/ab18-localstack.yaml`). The file must be owned by cloudflared's
user: `sudo chown 65532:65532 network/cloudflared_tunnel_token`. Ingress rules
are applied with `home cloud apply` (or `home route add|remove`).

Authelia (`id.ab18.in`) needs its secrets before the network stack starts. Seed
them from the Mac (idempotent; it prints the first-login password and the OIDC
client secrets once):

```bash
ssh dietpi@100.108.211.112 'bash -s -- <username> <email> "<display name>"' < scripts/authelia-secrets.sh
```

After that, users and OIDC clients are managed from the Mac with `home auth`
(`auth: {node: dietpi-l, url: https://id.ab18.in}` in `~/.config/home/config.yaml`):
`home auth user add|set|disable|rm …`, `home auth client add|set|rotate|rm …`,
`home auth code --wait` for one-time codes. Everything lives in
`secrets/network/authelia/` (`users.yml`, `clients.yml`, client secret digests).

### 3b — Data stack (postgres + redis)

```sh
put data/postgres_password "your-pg-password"
printf 'requirepass %s\n' "$(openssl rand -hex 24)" > data/redis.conf
sudo chown 999:1000 data/redis.conf       # the redis image's user
```

Immich authenticates to Redis with the same password (3c).

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
put immich/db_url "postgresql://immich:your-pg-password@postgres:5432/photos"
put immich/redis_password "$(sudo sed -n 's/^requirepass //p' data/redis.conf)"
```

### 3d — Kopia stack

```sh
put kopia/repository_password "your-kopia-repository-password"
put kopia/postgres_password "your-pg-password"      # for the pre-snapshot pg_dump
```

### 3e — Localstack stack (watchtower, heartbeat)

```sh
printf 'WATCHTOWER_UPDATE_ON_START=true\nWATCHTOWER_POLL_INTERVAL=300\n' > localstack.env
put localstack/hc_ping_url "https://hc-ping.com/<uuid>"
```

### 3f — Vaultwarden stack

```sh
put vaultwarden/database_url "postgresql://vaultwarden:your-vaultwarden-db-password@pg.ab18.in:443/ab18"
put vaultwarden/admin_token '$argon2id$…'            # vaultwarden hash (`vaultwarden hash`)
```

Sign-in is Authelia (OIDC client `vaultwarden`): run
`home auth client add vaultwarden --redirect https://vault.ab18.in/identity/connect/oidc-signin --name Vaultwarden --pkce`
in your own terminal, then put the secret it prints in place with
`home node edit dietpi-l secrets/vaultwarden/authelia_client_secret`.

### 3g — Memos stack

Memos connects as `memos@postgres:5432/memos` and signs users in through Authelia
(OIDC client `memos`). After the data stack is running:

```sh
docker exec -it postgres psql -U immich -d postgres -c "
  CREATE ROLE memos WITH LOGIN PASSWORD 'your-memos-db-password';
  CREATE DATABASE memos OWNER memos;
"
echo "MEMOS_DSN='postgres://memos:your-memos-db-password@postgres:5432/memos?sslmode=disable'" > memos.env
put memos/admin_password "$(openssl rand -hex 24)"
```

From the Mac, `home auth client add memos --name Memos --redirect
https://notes.ab18.in/auth/callback --policy one_factor --consent pre-configured
--scopes openid,profile,email` prints the client secret. Put it in
`memos/idp-authelia.json` (format: header of `nodes/ab/memos/compose.yaml` and
https://usememos.com/docs/configuration/deployment-configuration; identifier
`preferred_username`), then `sudo chown 10001:10001 memos/idp-authelia.json`:
Memos reads it as its container user.

The first account created on an empty Memos becomes its admin, whatever the
settings say. Create it before Memos is reachable: add `traefik.http.routers.notes.middlewares=auth`
to its labels for the first start, then, from the node:

```sh
ip=$(docker inspect -f '{{(index .NetworkSettings.Networks "internal_bridge").IPAddress}}' memos)
curl -s -X POST "http://$ip:5230/api/v1/users" -H 'Content-Type: application/json' \
  -d "{\"username\": \"admin\", \"password\": \"$(cat memos/admin_password)\"}"
```

and drop the middleware again.

### 3h — Treasure Dash

```sh
echo "DATABASE_URL='postgres://treasuredash:<password>@postgres:5432/treasuredash?search_path=treasuredash&sslmode=disable'" > treasuredash.env
```

### Complete secrets layout

```
/home/dietpi/localstack/secrets/
  data/postgres_password          data/redis.conf (owned by 999)
  immich/db_url                   immich/redis_password
  kopia/repository_password       kopia/postgres_password
  localstack.env                  localstack/hc_ping_url
  memos.env                       memos/admin_password
  memos/idp-authelia.json (owned by 10001)
  network/cf_dns_api_token        network/ts_auth_key
  network/cloudflared_tunnel_token (owned by 65532)
  network/authelia/               (home auth)
  syncthing/                      (Syncthing's identity: cert, key, device id)
  treasuredash.env
  vaultwarden/admin_token         vaultwarden/database_url
  vaultwarden/authelia_client_secret
```

---

## Phase 4 — Create Traefik acme.json

Traefik requires this file with strict permissions. It is node state, so it lives in
`data/`, outside the repo checkout.

```sh
mkdir -p /home/dietpi/localstack/data/traefik
touch /home/dietpi/localstack/data/traefik/acme.json
chmod 600 /home/dietpi/localstack/data/traefik/acme.json
```

---

## Phase 5 — First deploy (from your Mac)

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

## Phase 6 — Verify all containers

```sh
docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Image}}"
```

Expected running containers:
```
tailscale, traefik, whoami, cloudflared, authelia
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
| cloudflared `tunnel not found` | Wrong token in `secrets/network/cloudflared_tunnel_token` | Re-fetch with `cloudflared tunnel token ab18-localstack` and restart cloudflared |
| Immich fails to connect to DB | Password mismatch | `immich/db_url` must match `data/postgres_password` |
| Vaultwarden fails to connect to DB | `DATABASE_URL` password mismatch | Must match password used in `CREATE ROLE vaultwarden` |
| ESPHome config missing | `/home/dietpi/localstack/data/esphome/` not restored | Restore from backup (kopia) |
| Syncthing won't start | `/home/dietpi/localstack/data/syncthing/` not present | Restore from backup (kopia); an empty dir is seeded from `config.xml.template` |
| Agent not deploying a stack | Syntax error in compose | `docker compose -f nodes/ab/<stack>/compose.yaml config` |
