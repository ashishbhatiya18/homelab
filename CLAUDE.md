# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

A GitOps homelab monorepo. Two physical nodes (`ab` at 10.10.10.11, `cd` at 10.10.10.12) each run a set of Docker Compose stacks. Nodes have **no git checkout and no agent**: CI publishes one bundle per node (`ghcr.io/ashishbhatiya18/node-<node>`), and the `home` CLI on the Mac (`home-cli/`) deploys it over SSH — manually with `home deploy`, or automatically from its background job.

Directory layout:
- `nodes/<node>/<stack>/compose.yaml` — one stack per subdirectory, one file per stack
- `nodes/<node>/node.conf` — the node's start order (`ORDER=(…)`) and Docker networks (`network <name> <driver> [args…]`)
- `nodes/node.sh` — shared lifecycle script, shipped in every bundle next to the stacks: `start|stop|restart|recreate [stack…]`, `down <stack>`, `status`, `order`, `list`, `compose <stack> <args…>`. Every stack operation (by `home` or by hand) goes through it.
- `scripts/bundle.Dockerfile` — the node bundle (FROM scratch: `/stacks` = `nodes/<node>/` + `node.sh`)
- `home-cli/` — the `home` CLI (deploys, stack ops, node upgrades, backups); released as `home-cli-v*` tags to the Homebrew tap
- `webauthn-proxy/` — the passkey forward-auth proxy (image `ghcr.io/ashishbhatiya18/webauthn-proxy`)
- `terraform/` — Cloudflare DNS, tunnel ingress, and Tailscale ACLs (HCP Terraform remote state)
- `stackmgr-proxy/` — Go backend + Next.js frontend for a web UI to manage stacks

## Nodes and stacks

**Node ab** (amd64, primary): network (traefik + cloudflared + oauth2-proxy + tailscale), data (postgres + redis), immich, kopia, vaultwarden, media (jellyfin), filebrowser, homeautomation (esphome), syncthing, localstack (watchtower), excalidraw, rustpad, bentopdf, claudecode, codeserver, isponsorblock, stackmgr.

**Node cd** (ARM, Raspberry Pi 4): network (traefik + pihole + tailscale), localstack (dockerproxy + watchtower + arcane), citrusdental, costaebella, smiledesign.

All secrets live outside the repo at `/home/dietpi/localstack/secrets/` on each node and are referenced via Docker `secrets:` or `env_file:` entries. Node state (TLS certs, Pi-hole, Syncthing, ESPHome, databases…) lives in `/home/dietpi/localstack/data/`, never next to the compose files. Full per-node bootstrap procedures (registry access, secrets layout, first deploy, troubleshooting) are in `docs/deploy-ab.md` and `docs/deploy-cd.md`; CI/Terraform setup is in `docs/github-actions.md`.

## Networking

Networks used by stacks as `external`, declared in each `node.conf` and created by `node.sh start` before any stack starts:
- `internal_bridge` — used by the network/reverse-proxy stack and all app stacks that need Traefik routing
- `data-layer` (ab only) — isolated network for postgres/redis; only stacks that need DB access join it
- `pihole_macvlan` (cd only) — Pi-hole's LAN address 10.10.10.10 on eth0

## Traefik routing pattern

- All traffic arrives via Cloudflare tunnel (public) or Tailscale (internal-only services).
- Traefik terminates TLS and routes by `Host()` label.
- oauth2-proxy sits in front of most services via the `auth` middleware defined in `traefik_dynamic.yml`.
- Tunnel ingress rules (which subdomains are public) are **Terraform-managed** in `terraform/ab18.tf` — never edit them locally.

## Deploying changes

Push to `main`. `.github/workflows/node-bundles.yml` validates **every** compose file of each changed node and publishes its bundle (`:latest` and `:<sha>`). Then, from the Mac:

```sh
home deploy [dietpi-l|dietpi|all] [--dry-run]   # sync changed files, start changed stacks in order,
                                                 # health check, auto-rollback on failure
home deploy <node> --rollback                    # previous release
home jobs                                        # the background deploy job does this every 15 min
```

(`home` names the nodes `dietpi-l` = ab and `dietpi` = cd.) Images are never built on a node: stacks use `image:` only; custom images (kopia, claudecode, stackmgr-proxy, webauthn-proxy) are built by their own workflows.

On a node, by hand: `~/localstack/stacks/node.sh start|stop|restart|status [stack…]`. Each node's `/home/dietpi/localstack/` holds exactly: `stacks/` (deployed files, with `.release`), `releases/` (extracted bundles), `data/` (state) and `secrets/`.

## Terraform

State is in HCP Terraform (workspace `localstack-cloudflare`). Local use requires `terraform login` first.

```sh
cd terraform
terraform init
terraform plan
terraform apply
```

CI runs plan on PRs and plan+apply on pushes to `main`.

## CI checks

`.github/workflows/validate.yml` runs on every push/PR:
- **Compose validation**: runs `docker compose config --quiet` on any changed `compose.yaml`. Stub env files are generated automatically so missing secrets don't block validation.
- **Shell lint**: `shellcheck nodes/node.sh`

`node-bundles.yml` validates all compose files of a node again before publishing its bundle, so an invalid stack never reaches a node.

Always ensure `docker compose -f nodes/<node>/<stack>/compose.yaml config` passes before pushing a compose change.

## stackmgr-proxy

Go backend + Next.js frontend deployed as a Docker container on node ab (exposed at `stack.ab18.in`). It mounts `/var/run/docker.sock` and connects to node cd over SSH.

Local development:
```sh
# Backend
cd stackmgr-proxy/backend
go mod download
go run main.go

# Frontend
cd stackmgr-proxy/frontend
npm install
npm run dev        # http://localhost:3000
```

The backend image is built and pushed to GHCR by `.github/workflows/build-stackmgr-proxy.yml` on changes under `stackmgr-proxy/`.

To add a service health check, edit `getHealthCheckEndpoints()` in `stackmgr-proxy/backend/main.go`.

## Files intentionally not in git

Everything node-local is outside the deployed files, under `/home/dietpi/localstack/` on the node:
- `data/traefik/acme.json` — TLS certs (must be `chmod 600`), both nodes
- `data/esphome/`, `data/syncthing/` (ab), `data/pihole/` (cd) — app state; the repo's `syncthing/config.xml.template` and `config/pihole/` only seed a new node
- `secrets/` — env files, Docker secrets, and `secrets/oauth2-proxy/config.toml` (oauth2-proxy is disabled)
- `terraform/secrets.auto.tfvars` (on the Mac) — Terraform variable values
