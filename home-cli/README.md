# home — run your homelab from your Mac

`home` manages a small fleet of self-hosted machines and their Docker Compose
stacks over SSH — status, safe stack updates with rollback, apt and DietPi
upgrades, reboots that wait for everything to come back — and, as `home br`,
takes encrypted, portable Postgres backups you can restore anywhere.

```console
$ home status
NODE  DIETPI  OS           UPTIME  APT  REBOOT  DISK  CONTAINERS  ATTENTION
ab    10.7.2  13 (trixie)  15h     13   no      35%   24/24       13 apt updates
cd    10.7.2  13 (trixie)  21h     0    YES     75%   12/12       reboot required

$ home upgrade all
Plan:
  1. cd: update network → reboot if required
  2. ab: apt (13) → update network → reboot if required
```

No agent runs on your servers: `home` uses your existing SSH key, and needs
passwordless `sudo` for upgrades and reboots (setup checks both).

## Install

```console
brew install ashishbhatiya18/tap/home
home              # first run starts the interactive setup
home install      # background service: backups, daily node check, deploys, cleanup
```

## Nodes and stacks

| Command | What it does |
|---|---|
| `home status` | per node: DietPi/OS, uptime, pending apt and DietPi updates, reboot required, disk, unhealthy containers |
| `home node list` | nodes with SSH target, reachability, stack and container counts |
| `home stack list [node] [--updates]` | every stack with its health; `--updates` adds the image check |
| `home stacks [node]` | every stack's health and whether newer images exist (registry digest check — nothing is pulled) |
| `home check` | refresh package lists and run every check now |
| `home doctor [node]` | drift and risk: deployed bundle behind (or GitOps checkout behind), stray containers, stacks not running, missing restart policies, crash loops, unbounded logs, secrets inline in compose files, clock sync, Watchtower not monitor-only, Tailscale key and TLS certificate expiry |
| `home stack restart\|stop\|start <node>/<stack>` | lifecycle |
| `home stack logs <node>/<stack> [svc] [-f] [--tail N]` | logs |
| `home stack shell <node>/<stack> [svc]` | interactive shell in a container |
| `home stack exec <node>/<stack> [svc] -- <cmd…>` | run a command in a container (works in scripts and pipes too) |
| `home stack update <node>/<stack>` | pull → pre-update hook (e.g. a database backup) → apply → wait until healthy; **rolls back automatically** if not |
| `home stack rollback <node>/<stack>` | back to the images before the last update (old images are kept tagged) |
| `home node apt check\|upgrade <node\|all>` | apt, non-interactive, keeping your config files |
| `home node dietpi check\|upgrade <node\|all>` | DietPi's own updater, non-interactive |
| `home node reboot <node>` | reboot and wait until every container that was running is healthy again |
| `home node disk <node> [--clean]` | where the space goes (largest directories, Docker usage) and what can be reclaimed — dangling/unused images, build cache, old rollback images, oversized container logs, journal, apt cache — each chosen individually; nothing in use is touched |
| `home upgrade <node\|all>` | apt → DietPi → stack updates → reboot if required, node by node in your upgrade order; stops at the first failure |

Stacks are discovered from a directory per node with one subdirectory per
stack (each holding `compose.yaml`) — the layout used by most GitOps-style
homelab repos. If that directory has a `node.sh` (see below), every stack
operation goes through it: start/stop/restart in the node's start order, and
logs, exec, health checks and pulls via `node.sh compose <stack> …`.

## Deploys from node bundles

Instead of a git checkout and an agent on every node, CI can publish one
**bundle** per node: a `FROM scratch` image holding `nodes/<name>/` — that
node's stacks plus `node.sh` (lifecycle) and `node.conf` (start order and
networks). Set `bundle:` on the node in config.yaml and `stacks_dir` to
`<base>/nodes/<name>`; then:

| Command | What it does |
|---|---|
| `home deploy [node\|all] [--dry-run]` | pull the newest bundle on the node, sync changed files into `stacks_dir`, start the changed stacks in the node's start order (running their pre-update hooks first), wait until each is healthy; **rolls back** to the previous release if one is not |
| `home deploy <node> --tag <sha>` | deploy a specific bundle |
| `home deploy <node> --rollback` | back to the release before the current one |

Releases are extracted to `<base>/releases/<digest>/` (the newest five are
kept) and `stacks_dir/.release` records what is deployed. Changed files are
rewritten in place, so single-file bind mounts see them; files a release drops
are deleted; files that never came from a release are left alone. A stack
removed from the bundle keeps running until `node.sh down <stack>`.

**Private images**: `home registry login ghcr.io` saves a read-only token in
the macOS Keychain so private images are checked too.

**Auto-updaters**: if you run Watchtower, set `WATCHTOWER_MONITOR_ONLY=true`
so images only change when you run `home stack update` or `home upgrade`.

## Backups — `home br`

Encrypted ([age](https://age-encryption.org)), scheduled, portable Postgres
backups: dumps carry no owners or grants, so they restore cleanly into a fresh
server of the same or any newer version, and restore drills prove it.

| Command | What it does |
|---|---|
| `home br setup` | discovers your Postgres containers and databases, sets the schedule and keys |
| `home br status` / `home br snapshots` | what you have |
| `home br backup [app]` | back up now |
| `home br verify` | restore drill into throwaway Postgres (your version and the latest) |
| `home br restore <app> --snapshot yesterday` | restore to production (safety backup, app containers stopped, one transaction, auto-rollback) |
| `home br restore <app> --snapshot "1 month ago" --to postgres://admin@newhost/db --create` | restore into any Postgres |
| `home br restore <app> --local` | restore into a new local container to browse |

Configure `pre_update_hooks` (setup offers this) to take a fresh backup before
a stack's images are updated. See [docs/RESTORE.md](docs/RESTORE.md) for
restoring without `home`, e.g. after losing your Mac.

## Notifications

The background service (`home install`, managed by `brew services`) runs
every 15 minutes and runs each job when it is due (`home jobs` lists them,
`home jobs run <job>` runs one now):

- **Backups** (daily at your chosen time; catches up after sleep): ✓ after each
  backup; a warning with the reason when one can't run.
- **Node check** (daily): one summary — `ab: 13 apt updates · cd: reboot
  required · stack updates: network` — or "all nodes healthy".
- **Deploy** (every run; `jobs: {deploy: false}` turns it off): rolls a new
  bundle out to nodes that already run one, with the same health check and
  rollback as `home deploy`; notifies on every deploy and failure. A node's
  first deploy is always manual.
- **Cleanup** (daily, opt in with `jobs: {cleanup: true}`): reclaims what is
  re-created on demand — dangling/unused images, build cache, apt cache and
  rollback images older than 30 days. Never logs or the journal.

## Configuration

`~/.config/home/config.yaml` (nodes) and `~/.config/home/br.yaml` (backups),
both created by setup and free of secrets. Passwords and tokens live in the
macOS Keychain; backup keys live next to your backups.

Coming from `hbr`? `home br` picks up `~/.config/hbr` automatically (the
originals are kept); snapshots and keys are unchanged.

## Extending

Backups are config-only for new apps; new kinds of data are one file
implementing `source.Source` — see [docs/ADDING_SOURCES.md](docs/ADDING_SOURCES.md).

## License

MIT
