# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

A GitOps homelab monorepo. Two physical nodes (`ab` at 10.10.10.11, `cd` at 10.10.10.12) each run a set of Docker Compose stacks. A systemd `gitops-agent` service on each node polls this repo and redeploys any stack whose `compose.yaml` was touched in the latest push.

Directory layout:
- `nodes/<node>/<stack>/compose.yaml` — one stack per subdirectory, one file per stack
- `nodes/<node>/networks.sh` — creates Docker networks that stacks depend on
- `scripts/gitops-agent.sh` — the polling agent (deployed as a systemd unit)
- `scripts/install-agent.sh` — bootstrap script for a fresh node
- `terraform/` — Cloudflare DNS, tunnel ingress, and Tailscale ACLs (HCP Terraform remote state)
- `stackmgr-proxy/` — Go backend + Next.js frontend for a web UI to manage stacks

## Nodes and stacks

**Node ab** (amd64, primary): network (traefik + cloudflared + oauth2-proxy + tailscale), data (postgres + redis), immich, kopia, vaultwarden, media (jellyfin), filebrowser, homeautomation (esphome), syncthing, localstack (watchtower), excalidraw, rustpad, bentopdf, ollama, stackmgr.

**Node cd**: network (traefik + pihole + dnsdist), localstack, citrusdental.

All secrets live outside the repo at `/home/dietpi/localstack/secrets/` on each node and are referenced via Docker `secrets:` or `env_file:` entries.

## Networking

Each node has two Docker networks:
- `internal_bridge` — used by the network/reverse-proxy stack and all app stacks that need Traefik routing
- `data-layer` (ab only) — isolated network for postgres/redis; only stacks that need DB access join it

`nodes/<node>/networks.sh` creates these. They must exist before any stack starts.

## Traefik routing pattern

- All traffic arrives via Cloudflare tunnel (public) or Tailscale (internal-only services).
- Traefik terminates TLS and routes by `Host()` label.
- oauth2-proxy sits in front of most services via the `auth` middleware defined in `traefik_dynamic.yml`.
- Tunnel ingress rules (which subdomains are public) are **Terraform-managed** in `terraform/ab18.tf` — never edit them locally.

## Deploying changes

Push to `main`. The gitops-agent detects changed `compose.yaml` files and runs `docker compose up -d --remove-orphans` for each affected stack only. If the compose file has a `build:` directive, it rebuilds the image first.

To manually redeploy a stack on a node:
```sh
docker compose -f /home/dietpi/localstack/repo/nodes/<node>/<stack>/compose.yaml up -d --remove-orphans
```

To monitor the agent:
```sh
journalctl -u gitops-agent -f
```

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
- **Shell lint**: `shellcheck scripts/*.sh`

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

- `nodes/ab/network/config/traefik/acme.json` — TLS certs (must be `chmod 600`)
- `nodes/ab/network/config/oauth2-proxy/config.toml` real values (use `git update-index --skip-worktree` after filling in on node)
- `nodes/ab/homeautomation/config/` — ESPHome device configs
- `nodes/ab/syncthing/config/` — Syncthing state (partially committed)
- `terraform/secrets.auto.tfvars` — Terraform variable values
