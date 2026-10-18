# down

One command for everything you download: magnet links, `.torrent` files, YouTube
(and the ~1,800 other sites yt-dlp supports) and plain URLs. When a download
finishes, only what you asked for is left in the current directory: the torrent
metadata, control and partial files, thumbnails and subtitles (embedded
instead) and junk like `*.nfo`, `Sample/` or `RARBG.txt` are cleaned up.

```sh
brew install ashishbhatiya18/tap/down       # pulls in yt-dlp, deno, ffmpeg, aria2, sevenzip

down 'magnet:?xt=urn:btih:…'                # torrent; stops when done (never seeds)
down ubuntu.torrent https://host/x.torrent  # .torrent file or URL
down https://youtu.be/…                     # video, ≤1080p mp4 by default
down -a https://youtu.be/…                  # audio only (mp3)
down --playlist --items 1-10 -a 'https://youtube.com/watch?v=…&list=…'
down https://example.com/big.iso            # 16-connection download
down --select 'magnet:…'                    # choose files from the torrent
down url1 magnet2 file3.torrent             # several at once, one after another
```

Run `down help` for every option. What decides the engine: `magnet:` and `.torrent`
go to aria2; known media sites go to yt-dlp (YouTube Music, SoundCloud, Bandcamp
and Mixcloud default to audio); any other URL is probed and goes to yt-dlp if it
serves an HTML page, otherwise to aria2. `--engine torrent|media|http` overrides.

## Cleanup

| Download | Kept | Removed |
|---|---|---|
| torrent | the selected files; archives (zip/rar/7z, multi-part too) unpacked; a folder holding one file becomes that file | saved metadata (`<hash>.torrent`), aria2 control files, junk (never even downloaded when a whole file matches), the archives after unpacking |
| media | the final file(s) yt-dlp reports | fragments, `.part`/`.ytdl`, separate thumbnail/subtitle/format files |
| URL | the file (`--extract` unpacks it) | the aria2 control file |

`--keep-meta` keeps everything. `down clean <dir>` applies the junk/archive rules
to an existing folder (`--partial` also removes unfinished downloads); it shows
the plan and asks first.

Work happens in `./.down/<id>/` and moves out when complete, so nothing
half-done is ever mixed in with your files. Interrupt with Ctrl-C and run the
same command again to resume.

## Settings

Everything has a default; `down config` shows them and `down config --init`
writes `~/.config/down/config.yaml` to edit: audio format, video quality and
container, subtitles, SponsorBlock, the yt-dlp output template, connections,
speed limits, extra trackers, extraction, flattening and the junk patterns.
`down doctor` checks the tools.

## Linux (node ab)

```sh
curl -fsSL https://raw.githubusercontent.com/ashishbhatiya18/homelab/main/down-cli/install.sh | sh
```

Debian/Ubuntu, amd64 or arm64. It installs `down` from the newest `down-cli-v*`
GitHub release (checksum-verified) into `/usr/local/bin`, aria2, ffmpeg and 7zip
from apt, and the official standalone yt-dlp and deno (Debian's yt-dlp is too
old for YouTube). Re-run it to upgrade everything; `DOWN_VERSION=0.1.0` pins a
release. Then use `down` exactly as on the Mac: downloads land in the current
directory.
