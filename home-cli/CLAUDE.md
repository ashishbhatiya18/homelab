# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`home` is a single Go CLI, run on macOS, that manages self-hosted nodes over SSH (no agent on the servers) and has an embedded backup tool, `home br` (formerly the standalone `hbr`). It is public and installed via the Homebrew tap `ashishbhatiya18/homebrew-tap` (`brew install ashishbhatiya18/tap/home`). User config lives in `~/.config/home/config.yaml` (nodes) and `~/.config/home/br.yaml` (backups); nothing environment-specific is in the repo.

## Commands

```sh
make build                     # go build -trimpath -o home ./cmd/home
make vet && make test          # go vet ./... ; go test ./...
go test ./internal/br/store -run TestResolveAsOf   # single test
```

Point a dev build at scratch config instead of the real one:

```sh
HOME_CONFIG=/tmp/x/config.yaml HOME_BR_CONFIG=/tmp/x/br.yaml HOME_NO_NOTIFY=1 ./home status
HOME_BR_PASSPHRASE=…           # test-only: answers the backup-password prompt
```

All prompts read `/dev/tty`, so interactive flows (`home setup`, `home br setup`) can't be driven by piping stdin; use `expect`.

This project lives in `home-cli/` of the homelab repo. Releasing: push a `home-cli-vX.Y.Z` tag. `.github/workflows/home-cli.yml` (repo root) runs vet/tests, cross-builds `home` for darwin arm64/amd64, pushes both archives with `oras` as the package `ghcr.io/ashishbhatiya18/home-cli` (`:X.Y.Z`, `:latest`; each archive is a blob whose digest is its sha256), and rewrites `Formula/home.rb` in the tap repo to fetch them from `https://ghcr.io/v2/…/blobs/sha256:…` (Homebrew's GitHub Packages download strategy, as for its own bottles; `on_arm`/`on_intel`, explicit `version`) using the `TAP_DEPLOY_KEY` secret (a write deploy key scoped to the tap). It can be re-run by hand for an existing tag (`workflow_dispatch`, input `tag`) and is idempotent.

## Architecture

**Entry point** `cmd/home/main.go` dispatches top-level commands; `home br …` is forwarded to `internal/br/cli.Run`. Background jobs are separate Homebrew services: Homebrew allows one service per formula, so the release workflow writes companion formulae `home-backup`, `home-check`, `home-cleanup`, `home-deploy` into the tap, each installing a launcher that runs `home jobs run <job> --if-due` on its own interval (15 min, 15 min, 1 h, 5 min) with its own log. `--if-due` maps to `brcli.Scheduled`, `check.Daily`, `cleanupJob(false)` (daily after `checks.daily_at`) and `deployJob` (only nodes that already have a release). Daily jobs use "due since the last scheduled time" logic, so missed runs (sleep, off-network) catch up; job state is in `~/.local/state/home/jobs.json`. `home install|uninstall [job…]` (`internal/service`) installs the companion formulae and drives `brew services`, and unloads the single `home` service of versions before 0.5; `home run` remains for such old installs.

**Shared packages** (`internal/`): `remote` (wraps the system `ssh` with BatchMode, reusing the user's keys and `~/.ssh/config`), `notify` (osascript notifications), `prompt` (tty prompts), `service`.

**Node/stack side**
- `homecfg`: node config; `Ordered()` yields upgrade order (nodes hosting shared services go last).
- `node`: everything per host comes from one bash status script over SSH (DietPi/apt/reboot-required/disk/`docker ps -a`). Upgrades use `sudo -n` (passwordless sudo required). `Reboot` waits for a fresh uptime and then for every previously running container to be healthy.
- `stack`: a stack is `<stacks_dir>/<name>/compose.yaml` on a node; the compose project name equals the directory name. When `<stacks_dir>/node.sh` exists, every stack operation goes through it (`ComposeCmd` → `node.sh compose <stack> …`; start/stop/restart → `node.sh <action> <stack>`, which also creates the node's networks); otherwise compose is called directly. `Update` records image IDs, pulls, runs a pre-update hook (configured `pre_update_hooks`, executed as `home <args>` with the same binary — typically `br backup <app>`), applies, waits for health, and auto-rolls back by re-tagging the old image IDs. Old images are protected with `home-rollback/...` tags; rollback snapshots are JSON under `~/.local/state/home/rollback/`.
- `registry`: update detection compares a container's local `RepoDigests` (one SSH call per node) with the registry's tag digest via an HTTP `HEAD` (parallel, nothing pulled). Private registries use a login stored in the Keychain by `home registry login`. Watchtower is expected to run in monitor-only mode; `home` deliberately does not rely on Watchtower's logs.
- `deploy`: rolls node bundles (FROM-scratch images with `/stacks`, built by CI) out over SSH. `Prepare` pulls by digest, extracts to `releases/<digest>/stacks` next to `stacks_dir` and diffs against the live `stacks_dir` (files only present on the node are ignored); `Apply` rsyncs in place (`--checksum --inplace`, so single-file bind mounts update), deletes files the release dropped, writes `stacks_dir/.release`, then starts changed stacks by `node.sh phases` (ORDER stacks one at a time, the rest in one parallel `node.sh start a b c`) with pre-update hooks and `WaitHealthy` per stack in parallel; on failure it syncs the previous release back and restarts what it started. A flock in the state dir keeps manual deploys and the job apart.
- `check`: builds per-node reports for `home status`/`check`/`upgrade` and the single daily summary notification.
- `doctor`: one diagnostic bash script per node, evaluated locally into OK/Warn/Fail findings with hints; TLS expiry is checked from the Mac for every `Host()` in Traefik router labels (hosts that don't resolve locally are skipped). Env vars ending in `_FILE` are paths to secret files and are not flagged.
- `node/disk.go`: `home node disk` never offers anything in use — images used by any container, volumes and directories are report-only; `home-rollback/*` images are only offered past `--rollback-days`.
- `stack exec`/`shell` use `ssh -t` via `remote.Interactive` when attached to a terminal, and `docker compose exec -T` otherwise.

**Backup side (`internal/br/…`)**
- `source` is the extension point: a `Source` interface plus a type registry (`Register` in `init()`). Apps in `br.yaml` are lists of sources, so new apps are config-only and new data kinds are one new file. Optional interfaces: `Verifier` (restore drills) and `PortableRestorer` (`restore --to`/`--local`). Both Postgres types share `pgSource`; they differ only in the `pgExec` used to reach the database (`postgres-docker`: `docker exec` over SSH, no DB credentials; `postgres-url`: direct, password in the Keychain).
- Dumps are portable by design (`--no-owner --no-privileges --quote-all-identifiers`, custom format) so they restore into any same-or-newer Postgres without the original roles. When the local client tools are newer than a target server, `restoreLocalTools` converts the dump to SQL and strips settings the server doesn't know (e.g. `transaction_timeout` on < 17) before applying with `psql --single-transaction`.
- `engine`: `Backup` stages each source into a private temp dir, writes `manifest.json` (checksums, server major, row counts), then streams tar → age encryption → `store.Write`. `Restore` to production takes a `prerestore` safety backup, stops the configured app containers, restores in a single transaction, restarts, health-checks, and rolls back from the safety backup on failure. Production restores always require two typed confirmations; `--yes` only works with `--to`.
- `keys`: every backup is age-encrypted to two X25519 recipients — a daily key whose private half is scrypt-encrypted with the user's password, and a recovery key shown once at setup. Daily backups never need the password.
- `store`: files are `<dest>/<app>/<app>-<UTC timestamp>[-tag].tar.age` plus a `.sha256` sidecar, written to `.partial` then renamed. Tagged backups (`prerestore`, `targetcopy`) are never pruned. `Cutoff`/`Resolve` implement "as of" snapshot selectors (`yesterday`, `3 days ago`, dates).
- `retention`: GFS — newest per day, *oldest* per week/month (so a ~month-old copy always exists), plus `min_keep`.
- `cli/migrate.go` copies `~/.config/hbr` and `~/.local/state/hbr` to the new locations once, leaving the originals.

## Gotchas

- macOS `security find-generic-password -w` returns secrets containing newlines hex-encoded; store single-line values (registry logins are `user:token`, `parseLogin` still decodes the old v0.2.0 format).
- The Keychain service name stays `hbr` (`internal/br/secret`) so passwords saved by the old tool keep working.
- Retention/restore code assumes local time for day boundaries; snapshot file names are UTC.
