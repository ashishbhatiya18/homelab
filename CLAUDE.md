# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

A GitOps homelab monorepo. Two physical nodes (`ab` at 10.10.10.11, `cd` at 10.10.10.12) each run a set of Docker Compose stacks. Nodes have **no git checkout and no agent**: CI publishes one bundle per node (`ghcr.io/ashishbhatiya18/node-<node>`), and the `home` CLI on the Mac (`home-cli/`) deploys it over SSH — manually with `home deploy`, or automatically from its background job.

Directory layout:
- `nodes/<node>/<stack>/compose.yaml` — one stack per subdirectory, one file per stack
- `nodes/<node>/node.conf` — the node's start order (`ORDER=(…)`) and Docker networks (`network <name> <driver> [args…]`)
- `nodes/node.sh` — shared lifecycle script, shipped in every bundle next to the stacks: `start|stop|restart|recreate [stack…]`, `down <stack>`, `status`, `order`, `list`, `compose <stack> <args…>`. Every stack operation (by `home` or by hand) goes through it.
- `scripts/bundle.Dockerfile` — the node bundle (FROM scratch: `/stacks` = `nodes/<node>/` + `node.sh`)
- `home-cli/` — the `home` CLI (deploys, stack ops, node upgrades, backups, Authelia users and OIDC clients via `home auth`); released as `home-cli-v*` tags to the Homebrew tap
- `down-cli/` — the `down` downloader CLI (magnets, torrents, YouTube, URLs; cleans up after); released as `down-cli-v*` tags to the Homebrew tap (Mac) and as GitHub release binaries that `down-cli/install.sh` installs natively on node ab
- `cloud/` — Cloudflare tunnels, ingress routes and DNS, applied with `home cloud`

## Nodes and stacks

**Node ab** (amd64, primary): network (traefik + cloudflared + authelia + tailscale), data (postgres + redis), immich, kopia, vaultwarden, media (jellyfin), filebrowser, homeautomation (esphome), syncthing, localstack (watchtower), excalidraw, rustpad, bentopdf, claudecode, codeserver, isponsorblock, treasuredash, ntfy (push notifications, public at ntfy.ab18.in with its own token auth). Outside the stacks, the `down` CLI is installed natively in `/usr/local/bin` (`down-cli/install.sh`).

**Node cd** (ARM, Raspberry Pi 4): network (traefik + pihole + tailscale), localstack (watchtower), monitor (gatus: health checks for both nodes, alerts via ntfy), citrusdental, costaebella, smiledesign.

All secrets live outside the repo at `/home/dietpi/localstack/secrets/` on each node and are referenced via Docker `secrets:` or `env_file:` entries. Node state (TLS certs, Pi-hole, Syncthing, ESPHome, databases…) lives in `/home/dietpi/localstack/data/`, never next to the compose files. Full per-node bootstrap procedures (registry access, secrets layout, first deploy, troubleshooting) are in `docs/deploy-ab.md` and `docs/deploy-cd.md`; CI workflows are described in `docs/github-actions.md`, Cloudflare under "Cloudflare" below.

## Networking

Networks used by stacks as `external`, declared in each `node.conf` and created by `node.sh start` before any stack starts:
- `internal_bridge` — used by the network/reverse-proxy stack and all app stacks that need Traefik routing
- `data-layer` (ab only) — isolated network for postgres/redis; only stacks that need DB access join it
- `pihole_macvlan` (cd only) — Pi-hole's LAN address 10.10.10.10 on eth0

## Traefik routing pattern

- All traffic arrives via Cloudflare tunnel (public) or Tailscale (internal-only services).
- Traefik terminates TLS and routes by `Host()` label.
- Authelia (`id.ab18.in`) sits in front of most services via the `auth` forward-auth middleware, defined by labels on its container in `nodes/ab/network/compose.yaml`; it is also the OIDC provider (Immich). Users and OIDC clients: `home auth`.
- Tunnel ingress rules (which subdomains are public) live in `cloud/tunnels/*.yaml` and are applied with `home cloud apply` (or `home route add|remove`).

## Deploying changes

Push to `main`. `.github/workflows/node-bundles.yml` validates **every** compose file of each changed node and publishes its bundle (`:latest` and `:<sha>`). Then, from the Mac:

```sh
home deploy [dietpi-l|dietpi|all] [--dry-run]   # sync changed files, start changed stacks in order,
                                                 # health check, auto-rollback on failure
home deploy <node> --rollback                    # previous release
home jobs                                        # the background deploy job does this every 15 min
```

(`home` names the nodes `dietpi-l` = ab and `dietpi` = cd.) Images are never built on a node: stacks use `image:` only; custom images (kopia, claudecode) are built by their own workflows.

On a node, by hand: `~/localstack/stacks/node.sh start|stop|restart|status [stack…]`. Each node's `/home/dietpi/localstack/` holds exactly: `stacks/` (deployed files, with `.release`), `releases/` (extracted bundles), `data/` (state) and `secrets/`.

## Cloudflare

Tunnels, their ingress routes and DNS for every zone are declared in `cloud/` (`tunnels/<name>.yaml`, `zones/<zone>.yaml`) and applied from the Mac with `home cloud` — not by CI:

```sh
home cloud plan            # what would change to make Cloudflare match cloud/
home cloud apply           # do it (snapshot first; `home cloud undo` reverts)
home route add|set|remove <host>   # edit the files and apply in one step
```

Cloudflare Pages projects, cache rules and Tailscale settings (split DNS, auth keys) are managed in their dashboards.

## CI checks

`.github/workflows/validate.yml` runs on every push/PR:
- **Compose validation**: runs `docker compose config --quiet` on any changed `compose.yaml`. Stub env files are generated automatically so missing secrets don't block validation.
- **Shell lint**: `shellcheck nodes/node.sh`

`node-bundles.yml` validates all compose files of a node again before publishing its bundle, so an invalid stack never reaches a node.

Always ensure `docker compose -f nodes/<node>/<stack>/compose.yaml config` passes before pushing a compose change.

## Files intentionally not in git

Everything node-local is outside the deployed files, under `/home/dietpi/localstack/` on the node:
- `data/traefik/acme.json` — TLS certs (must be `chmod 600`), both nodes
- `data/esphome/`, `data/syncthing/` (ab), `data/pihole/` (cd) — app state; the repo's `syncthing/config.xml.template` and `config/pihole/` only seed a new node
- `secrets/` — env files, Docker secrets, and `secrets/authelia/` (Authelia's secrets, `users.yml` and `clients.yml`, managed with `home auth`)
