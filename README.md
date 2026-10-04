# hbr — homelab backup & restore

Encrypted, scheduled, **portable** Postgres backups for self-hosted apps, run
from your Mac — and restores into **any** Postgres, from a single command.

```console
$ hbr restore myapp --snapshot yesterday --to postgres://admin@newhost:5432/myapp --create
```

- **Zero-credential backups** for databases in Docker: `pg_dump` runs inside
  the container over your existing SSH key. Direct connections are supported
  too, with the password kept in the macOS Keychain.
- **Portable snapshots**: dumps carry no owners or grants, so they restore
  cleanly into a fresh server of the same or any newer Postgres version.
  Restore drills prove it by restoring into throwaway Postgres containers
  (your version *and* the latest).
- **Encrypted** with [age](https://age-encryption.org) to two keys: a daily key
  protected by your password, and a recovery key you keep elsewhere. Daily
  backups never need the password; reading a backup always does.
- **Scheduled** via `brew services`, catches up after sleep or travel, and
  **notifies** you of every success and of anything that stops a backup.
- **Retention** 7 daily / 4 weekly / 12 monthly (configurable), always keeping a
  copy from yesterday and one about a month old.

## Install

```console
brew install ashishbhatiya18/tap/hbr
hbr            # first run starts the interactive setup
hbr install    # run in the background (managed by brew services)
```

Requirements: macOS with Homebrew. For Docker-hosted databases, passwordless
SSH to the server (the setup checks this and tells you how to fix it). For
restore drills, Docker or Podman.

## Setup

`hbr` (or `hbr setup`) asks a few questions and discovers the rest:

1. where to keep backups — a synced folder like Dropbox gives you an off-site
   copy for free (backups are useless without your keys);
2. the daily backup time and retention;
3. your server: it verifies passwordless SSH, finds the Postgres container and
   lists its databases so you just pick them;
4. optionally, which app containers to stop during a production restore;
5. a password, then a **recovery key shown once** — save it in a password
   manager you can reach from another device. Setup asks you to paste it back
   to prove it was saved.

It writes `~/.config/hbr/config.yaml` (no secrets in it), can take the first
backup, run a restore drill, and install the service.

## Everyday use

| Command | What it does |
|---|---|
| `hbr status` | last backup, snapshot count, last drill, problems, server and service state |
| `hbr snapshots [app]` | list snapshots |
| `hbr backup [app…]` | take a backup now |
| `hbr verify [app…]` | restore drill into throwaway Postgres; asks your password |
| `hbr restore <app> --snapshot S` | restore to the app's own database (safety backup first, app containers stopped, single transaction, automatic rollback) |
| `hbr restore <app> --snapshot S --to URL` | restore into **any** Postgres (`--create`, `--replace`, `--password-stdin`, `--yes`) |
| `hbr install` / `hbr uninstall` | start / stop the background service (`brew services`) |
| `hbr passwd` | change the password; existing snapshots stay readable |

Snapshot selectors pick the newest snapshot **as of** that point: `latest`,
`yesterday`, `"3 days ago"`, `"2 weeks ago"`, `"1 month ago"`, `2026-10-03`,
`"2026-10-03 14:30"`, or an exact file name.

## Notifications

| When | Notification |
|---|---|
| A scheduled backup succeeds | ✓ app, size, rows (turn off with `alerts.notify_success: false`) |
| A due backup can't run (server unreachable, SSH, keys, dump error…) | warning with the reason — immediately, then every 6 h while it persists |
| A backup succeeds after a problem | ✓ "earlier problem resolved" |
| No backup for 2 days | stale warning |
| Every 30 days | reminder to run a restore drill |

## If you lose this Mac

Everything needed is in your backup folder plus either your password or the
recovery key. See [docs/RESTORE.md](docs/RESTORE.md) for restoring with only
`age` and Postgres tools on any machine.

## Extending

Apps are config-only. New kinds of data (MySQL, SQLite, volumes…) are one file
implementing the `source.Source` interface — see
[docs/ADDING_SOURCES.md](docs/ADDING_SOURCES.md).

## License

MIT
