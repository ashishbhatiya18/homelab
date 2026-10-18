#!/bin/sh
# Installs (or upgrades) down on a Debian/Ubuntu machine such as node ab:
#   - down itself, from the newest down-cli-v* GitHub release (or $DOWN_VERSION)
#   - aria2, ffmpeg and 7zip from apt
#   - yt-dlp and deno as their official standalone binaries (Debian's yt-dlp
#     is too old for YouTube; yt-dlp needs deno as its JavaScript runtime)
# Everything goes to /usr/local/bin. Re-run it to upgrade; it is idempotent.
#
#   curl -fsSL https://raw.githubusercontent.com/ashishbhatiya18/homelab/main/down-cli/install.sh | sh
set -eu

REPO=ashishbhatiya18/homelab
BIN=/usr/local/bin

say() { printf '==> %s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "Linux only; on macOS: brew install ashishbhatiya18/tap/down"
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64; ytdlp=yt-dlp_linux;         deno=x86_64 ;;
  aarch64|arm64) arch=arm64; ytdlp=yt-dlp_linux_aarch64; deno=aarch64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac
if [ "$(id -u)" -eq 0 ]; then sudo=""; else sudo="sudo"; fi
command -v curl >/dev/null || die "curl is required"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "apt: aria2 ffmpeg 7zip unzip"
$sudo apt-get update -qq
$sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq aria2 ffmpeg 7zip unzip ca-certificates >/dev/null

say "yt-dlp (latest)"
curl -fsSLo "$tmp/yt-dlp" "https://github.com/yt-dlp/yt-dlp/releases/latest/download/$ytdlp"
$sudo install -m 755 "$tmp/yt-dlp" "$BIN/yt-dlp"

say "deno (latest)"
curl -fsSLo "$tmp/deno.zip" "https://github.com/denoland/deno/releases/latest/download/deno-$deno-unknown-linux-gnu.zip"
unzip -qo "$tmp/deno.zip" deno -d "$tmp"
$sudo install -m 755 "$tmp/deno" "$BIN/deno"

tag="${DOWN_VERSION:+down-cli-v${DOWN_VERSION#v}}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=100" |
        grep -o '"tag_name": *"down-cli-v[^"]*"' | sed 's/.*"\(down-cli-v[^"]*\)"/\1/' |
        sort -V | tail -1)
  [ -n "$tag" ] || die "no down-cli release found on $REPO"
fi
say "down ${tag#down-cli-v}"
base="https://github.com/$REPO/releases/download/$tag"
curl -fsSLo "$tmp/down.tar.gz" "$base/down_linux_$arch.tar.gz"
curl -fsSLo "$tmp/checksums.txt" "$base/checksums.txt"
want=$(awk -v f="down_linux_$arch.tar.gz" '$2 == f { print $1 }' "$tmp/checksums.txt")
got=$(sha256sum "$tmp/down.tar.gz" | cut -d' ' -f1)
[ -n "$want" ] && [ "$want" = "$got" ] || die "checksum mismatch for down_linux_$arch.tar.gz"
tar -xzf "$tmp/down.tar.gz" -C "$tmp" down
$sudo install -m 755 "$tmp/down" "$BIN/down"

"$BIN/down" doctor || true
