# CLAUDE.md

## What this is

`down` is a Go CLI that downloads magnet links, torrents, media pages and plain URLs into the current directory and then removes everything but the payload. It drives external tools rather than embedding them: **aria2c** (torrents + HTTP/FTP, over JSON-RPC) and **yt-dlp** (+ ffmpeg, deno). It runs on macOS (Homebrew tap `ashishbhatiya18/homebrew-tap`, `brew install ashishbhatiya18/tap/down`) and natively on Linux such as node ab (`install.sh`: the binary from the GitHub release, aria2/ffmpeg/7zip from apt, standalone yt-dlp and deno, all in `/usr/local/bin`; re-run to upgrade).

## Commands

```sh
make build && make vet && make test
DOWN_CONFIG=/tmp/x.yaml ./down …   # scratch config; XDG_CACHE_HOME moves the DHT cache
```

`internal/aria`'s resume test starts a real aria2c (skipped when it isn't installed). Prompts (`--select`, `down clean`) read `/dev/tty`.

Releasing: push a `down-cli-vX.Y.Z` tag. `.github/workflows/down-cli.yml` tests, publishes darwin arm64/amd64 archives with `oras` as `ghcr.io/ashishbhatiya18/down-cli` and rewrites `Formula/down.rb` in the tap (same scheme as home-cli), and attaches linux amd64/arm64 archives plus `checksums.txt` to a GitHub release named after the tag (`--latest=false`; `install.sh` picks the highest `down-cli-v*` itself). `doctor`'s fix hints are Homebrew on macOS and the install script elsewhere.

## Architecture

- `cmd/down`: `main.go` (flag parsing that allows flags after URLs, the per-kind flows `media`/`torrent`/`http`, `finish`/`publish`, the torrent file `selector`), `tools.go` (`doctor`, `config`, `clean`).
- `detect`: `Parse` classifies without network (magnet → info hash, base32 normalized; `.torrent` path/URL; known media hosts; else `Guessed` HTTP), `Probe` turns a guessed URL serving HTML into media. `ID(mode)` names the staging dir: the info hash for magnets, so the same torrent resumes whatever its trackers.
- Flow: everything downloads into `<out>/.down/<id>/` (`clean.StagingDir`; same filesystem, so publishing is a rename). On success `clean` tidies it, `Publish` moves the payload into `<out>` (never overwriting: `name (1).ext`), and the staging dir is removed; whatever was left there is reported as cleaned. On failure or Ctrl-C the staging dir stays, which is what makes re-running resume.
- `aria`: one aria2c per run, RPC on 127.0.0.1 with a random secret, its own process group (Ctrl-C reaches only us; `Close` → `forceShutdown` saves `.aria2` control files), `--stop-with-process`. `--seed-time=0`, `--follow-torrent=mem` + `--pause-metadata`: a magnet's metadata download completes with `followedBy` → the real download starts paused; `Download` follows the chain and, once a torrent's file list is known, applies `Select` (pausing an active download first, since aria2 only changes `select-file` on paused ones), then unpauses. `--bt-save-metadata`/`--bt-load-saved-metadata` keep `<hash>.torrent` in staging so a resumed magnet skips the metadata step; `--bt-remove-unselected-file` drops partial pieces of unselected files.
- `ytdl`: builds yt-dlp args (`--quiet --progress` with `--progress-template` lines tagged `[down]`/`[down-pp]`, `--print after_move:[down-file] %(filepath)s` for the final paths, `--no-playlist` unless `--playlist`, `--format-sort res:<h>[,vcodec:h264,acodec:m4a for mp4]`). Only the reported files are published, so all of yt-dlp's side files go with staging.
- `clean`: `Match`/`IsJunk` (case-insensitive globs on each path component), `TidyTorrent` (metadata at the staging root; inside each top-level folder — the torrent's own folder name never counts as junk), `Tidy` (junk, `Partial` leftovers, archives, empty dirs; `DryRun` for `down clean`), `Volumes` (first volume + the regex of the whole set: `.partN.rar`, `.rar`+`.rNN`, `.7z.001`), `Extractor` (7zz/7z, unar, bsdtar), `Flat`, `Publish`, `Unique`.
- `config`: `~/.config/down/config.yaml` (`$DOWN_CONFIG`, `$XDG_CONFIG_HOME`) over `Default()`; no output dirs by design (downloads go to the current directory or `-o`). Cache (`dht.dat`) in `~/.cache/down`.
