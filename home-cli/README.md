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
passwordless `sudo` for upgrades and reboots (setup checks both). Give a node
a second address with `tailscale:` (e.g. `dietpi@100.x.y.z`) and every command
uses it whenever the `ssh:` address doesn't answer — off the LAN.

## Install

```console
brew install ashishbhatiya18/tap/home
home              # first run starts the interactive setup
home install      # background jobs as brew services: backup, check, cleanup, deploy
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
| `home stack update <node\|all> [--dry-run]` | every stack with a newer image, one at a time in start order (node.conf's `ORDER` first); a failed app stack is rolled back and the rest go ahead, a failed `ORDER` stack stops the run. Over Tailscale, a stack running Tailscale itself is skipped |
| `home stack rollback <node>/<stack>` | back to the images before the last update (old images are kept tagged) |
| `home node apt check\|upgrade <node\|all>` | apt, non-interactive, keeping your config files |
| `home node dietpi check\|upgrade <node\|all>` | DietPi's own updater, non-interactive |
| `home node reboot <node>` | reboot and wait until every container that was running is healthy again |
| `home node disk <node> [--clean]` | where the space goes (largest directories, Docker usage) and what can be reclaimed — dangling/unused images, build cache, old rollback images, oversized container logs, journal, apt cache — each chosen individually; nothing in use is touched |
| `home node edit <node> <path> [--sudo]` | open a file on the node in `$VISUAL`/`$EDITOR` and write it back in place if it changed; relative paths start next to `stacks_dir` (`secrets/app.env`); refuses to overwrite a file that changed meanwhile |
| `home upgrade <node\|all>` | apt → DietPi → stack updates → reboot if required, node by node in your upgrade order; stops at the first failure |

Stacks are discovered from a directory per node with one subdirectory per
stack (each holding `compose.yaml`) — the layout used by most GitOps-style
homelab repos. If that directory has a `node.sh` (see below), every stack
operation goes through it: start/stop/restart in the node's start order, and
logs, exec, health checks and pulls via `node.sh compose <stack> …`.

## Deploys from node bundles

Instead of a git checkout and an agent on every node, CI can publish one
**bundle** per node: a `FROM scratch` image holding `/stacks` — that node's
stacks plus `node.sh` (lifecycle) and `node.conf` (start order and networks).
Set `bundle:` on the node in config.yaml and `stacks_dir` to e.g.
`<base>/stacks`; then:

| Command | What it does |
|---|---|
| `home deploy [node\|all] [--dry-run]` | pull the newest bundle on the node, sync changed files into `stacks_dir`, start the changed stacks phase by phase — the node's `ORDER` stacks one at a time, then all others in parallel — running their pre-update hooks first and waiting until every stack of a phase is healthy; **rolls back** to the previous release if one is not |
| `home deploy <node> --tag <sha>` | deploy a specific bundle |
| `home deploy <node> --rollback` | back to the release before the current one |
| `home deploy <node> --recreate` | every stack with fresh containers, e.g. after moving `stacks_dir` |
| `home secret migrate <node>/<stack> [--dry-run] [--yes] [--keep-old]` | move a stack's secret files into a new layout: follows the `secrets.migrate.yaml` its release ships (copy a file or directory, one variable of an env file into its own file, or some lines into a new env file — all done on the node, values never cross SSH), deploys, then deletes the old files no deployed stack references; a failed deploy rolls back and removes the new files |

Releases are extracted to `releases/<digest>/` next to `stacks_dir` (the
newest five are kept) and `stacks_dir/.release` records what is deployed. Changed files are
rewritten in place, so single-file bind mounts see them; files a release drops
are deleted; files that never came from a release are left alone. A stack
removed from the bundle keeps running until `node.sh down <stack>`.

**Private images**: `home registry login ghcr.io` saves a read-only token in
the macOS Keychain so private images are checked too.

**Phone notifications**: `home notify setup` saves an [ntfy](https://ntfy.sh)
topic URL, its access token and an optional fallback topic (e.g. on ntfy.sh,
used when your own ntfy server is unreachable) in the Keychain. Every
notification is then pushed there as well as shown on the Mac; `home notify
test` sends one, `home notify off` removes the settings.

**Auto-updaters**: if you run Watchtower, set `WATCHTOWER_MONITOR_ONLY=true`
so images only change when you run `home stack update` or `home upgrade`.

## Cloudflare Tunnels, routes and DNS

`home` reads your Cloudflare Tunnels, their ingress rules and your zones' DNS
through the API, finds which `cloudflared` container on which node runs each
tunnel (from its token's tunnel ID, decoded on the node — the token never
leaves it), and matches every route with the Traefik router behind it.

| Command | What it does |
|---|---|
| `home cloud setup` | saves an API token in the Keychain, picks the account and the directory for the files |
| `home tunnel list` | every tunnel: health, connectors and edge locations, the node/stack running it, route count, whether it has a file |
| `home tunnel show <tunnel>` | connectors (version, arch, uptime, edge, origin IP), the container running it, and each ingress rule with its origin settings and DNS state |
| `home route list [tunnel\|node\|zone] [--all]` | every route: tunnel, service, DNS, Traefik router, forward-auth — and drift: routes without DNS or without a router, CNAMEs to a tunnel that doesn't route them or to a deleted tunnel; `--all` adds the LAN/Tailscale-only hosts |
| `home route check [host…]` | each route end to end *through Cloudflare's edge* (resolved via 1.1.1.1, so local DNS can't short-cut it): DNS → tunnel → ingress → HTTP status, explaining failures (tunnel down, origin unreachable, nothing matched) |
| `home cloud export [--dir <dir>]` | writes the live configuration to `tunnels/<name>.yaml` and `zones/<zone>.yaml`, for review and commit |
| `home cloud plan` | what would change to make Cloudflare match the files |
| `home cloud apply [--yes]` | makes it so, after a snapshot of everything it touches; stops at the first error |
| `home cloud undo [--list] [snapshot]` | puts back the state from before the last (or a given) apply |
| `home route add <host> [--tunnel t] [origin flags]` | routes a hostname (with its CNAME): a bare hostname when the tunnel's defaults fit, otherwise a full rule — `--service`, `--host-header`, `--server-name`, `--no-tls-verify`, `--http2`, `--path`; warns when no Traefik router answers the host yet |
| `home route set <host> <origin flags>` / `remove <host>` / `move <host> --to <tunnel>` | change, drop (with its CNAME) or move a route |
| `home dns add <name> <type> <content> [--proxied] [--ttl N] [--priority N] [--comment C]` / `dns remove <name> [type] [content]` | other records |
| `home tunnel create <name> --node N --stack S [--container C] [--secret PATH]` | a new tunnel and its file; writes the token to the node and prints the compose service to add |
| `home tunnel token <tunnel> --install [--secret PATH]` | token → node file (over SSH stdin, never printed), recreate the connector, wait until it connects |
| `home tunnel rotate <tunnel>` | new tunnel secret, then the same install — the old token stops working |
| `home tunnel delete <tunnel>` | refuses while a route, a DNS record or a running connector still uses it |

The files are meant for git. A tunnel file uses cloudflared's own ingress
fields; routes sharing a service and origin settings collapse into `defaults`
(`{host}` stands for each route's hostname), so a typical tunnel reads:

```yaml
tunnel: ab18-localstack
node: ab
stack: network
container: cloudflared
defaults:
  service: https://traefik:443
  originRequest: {httpHostHeader: '{host}', originServerName: '{host}'}
originRequest: {http2Origin: true, noTLSVerify: false}
ingress:
  - auth.ab18.in
  - photos.ab18.in
  - hostname: ntfy.ab18.in        # a full rule stands alone
    service: http://ntfy:80
  - service: http_status:404      # the last rule matches everything
```

Route and DNS commands edit the files (comments survive) and then run plan →
apply; `--no-apply` only edits, `--yes` skips the question. Commit the files
afterwards: they are the record of what Cloudflare should look like, and
`home doctor` and the daily check report any difference, connectors that are
down, and routes without DNS or a router.

Applies never leave a gap: a tunnel first gains new rules while keeping the
ones on their way out, DNS changes next (a record of another type is removed
right before its replacement), and only then are old rules dropped — so a
route moving between tunnels never meets the catch-all. A tunnel file's
`secret:` says where its connector's token lives on the node: a file holding
just the token (`TUNNEL_TOKEN_FILE`, found automatically) or an env file with a
`TUNNEL_TOKEN=` line; it is rewritten in place, so single-file mounts see it.

A zone file lists the zone's other records; the proxied CNAMEs that tunnel
routes need are implied by the tunnel files. Names matching the zone's
`ignore:` patterns (and always `_acme-challenge*`) belong to something else and
are left out; `ignore:` survives a re-export. `home route check` counts a 4xx at
`/` as reachable (a warning, as APIs often have nothing there) and anything
from 500 up, or a Cloudflare error, as a failure. The token needs **Account ·
Cloudflare Tunnel** and **Zone · DNS** (Read is enough for everything above)
plus **Zone · Zone · Read**.

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

Background jobs run as Homebrew services, one per job — `home install`
installs and starts them all (`home install backup check` picks some),
`home jobs` shows each one's state and last run, `home jobs run <job>` runs one
now, and `brew services list` shows them like any other service. Each logs to
`$(brew --prefix)/var/log/home-<job>.log`.

| brew service | Every | Does |
|---|---|---|
| `home-backup` | 15 min | **Backups** when due (daily at your chosen time; catches up after sleep): ✓ after each backup; a warning with the reason when one can't run |
| `home-check` | 15 min | **Node check** once a day after `checks.daily_at`: one summary — `ab: 13 apt updates · cd: reboot required · stack updates: network` — or "all nodes healthy" |
| `home-cleanup` | 1 h | **Pruning** once a day: what is re-created on demand — dangling/unused images, build cache, apt cache and rollback images older than 30 days. Never logs or the journal |
| `home-deploy` | 5 min | **Deploys** a new bundle to nodes that already run one, with the same health check and rollback as `home deploy`; notifies on every deploy and failure (a release waiting for `home secret migrate` once). A node's first deploy is always manual |

(Homebrew allows one service per formula, so each job is a small companion
formula in the tap that runs `home jobs run <job> --if-due`.)

## Configuration

`~/.config/home/config.yaml` (nodes) and `~/.config/home/br.yaml` (backups),
both created by setup and free of secrets. A node (config.yaml `nodes:`) or
backup host (br.yaml `hosts:`) may have `tailscale: user@100.x.y.z` next to
`ssh:`; it is used when `ssh:` doesn't accept a connection within 1.5 s, and
that choice holds for the rest of the command. Passwords and tokens live in the
macOS Keychain; backup keys live next to your backups.

Coming from `hbr`? `home br` picks up `~/.config/hbr` automatically (the
originals are kept); snapshots and keys are unchanged.

## Extending

Backups are config-only for new apps; new kinds of data are one file
implementing `source.Source` — see [docs/ADDING_SOURCES.md](docs/ADDING_SOURCES.md).

## License

MIT
