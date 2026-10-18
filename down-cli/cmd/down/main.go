// Command down downloads magnet links, torrents, YouTube (and other media
// sites) and plain URLs, then leaves only the payload: torrent metadata,
// control files, partial files and junk are cleaned up.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ashishbhatiya18/down/internal/aria"
	"github.com/ashishbhatiya18/down/internal/clean"
	"github.com/ashishbhatiya18/down/internal/config"
	"github.com/ashishbhatiya18/down/internal/detect"
	"github.com/ashishbhatiya18/down/internal/ui"
	"github.com/ashishbhatiya18/down/internal/ytdl"
)

var version = "dev"

const usage = `down — download magnet links, torrents, YouTube and direct URLs, then clean up

Usage:
  down <url|magnet|file.torrent>… [options]
  down clean <dir> [--partial] [--no-extract] [--yes]
  down doctor                   check yt-dlp, ffmpeg, aria2c and the archive tool
  down config [--init]          show the settings (--init writes the defaults to edit)
  down version

What handles what:
  magnet:…, *.torrent (file or URL)   aria2; stops as soon as it's done (never seeds)
  YouTube and other media sites       yt-dlp: video by default, audio by default for
                                      YouTube Music, SoundCloud, Bandcamp, Mixcloud
  any other http(s)/ftp URL           aria2, many connections; a URL that serves an
                                      HTML page goes to yt-dlp instead

Media:
  -a, --audio             audio only
  -v, --video             video (overrides the audio default of music sites)
  --format F              audio format: mp3 m4a opus flac best      [audio_format]
  --quality Q             video: 480p 720p 1080p 1440p 4k best       [video_quality]
  --container C           video: mp4 mkv                             [container]
  --subs LANGS            embed subtitles, e.g. en or en,hi          [subs]
  --sponsorblock          cut sponsor segments                       [sponsorblock]
  --playlist              the whole playlist of a watch?v=…&list=… URL
  --items 1-5,8           only these playlist items
  --template T            yt-dlp output template                     [template]

Torrents and files:
  --select                choose which files of a torrent to download
  --max-down R            speed limits, e.g. 5M                      [max_down]
  --max-up R                                                         [max_up]
  --extract, --no-extract unpack zip/rar/7z and delete the archives  [extract]
                          (default: torrents per config, URLs off)
  --no-flatten            keep a folder that holds a single file     [flatten]

General:
  -o, --out DIR           output directory (default: the current directory)
  --engine E              force torrent, media or http
  --keep-meta             skip cleanup: keep .torrent, junk, archives, thumbnails
  --verbose               show yt-dlp's output

Cleanup keeps only what you downloaded: the .torrent/magnet metadata, aria2 control
files, yt-dlp fragments, thumbnails and subtitles (embedded instead), and junk such
as *.nfo, *.txt, Sample/ and RARBG files are deleted ([junk] in down config).

Downloads happen in <out>/.down/ and move out when done. An interrupted download
resumes when you run the same command again.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	case "version", "--version":
		fmt.Println("down", version)
		return
	case "doctor":
		os.Exit(doctor())
	case "config":
		err = configCmd(args[1:])
	case "clean":
		err = cleanCmd(args[1:])
	default:
		os.Exit(get(args))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "down:", err)
		os.Exit(1)
	}
}

// parse lets flags and positional arguments mix (down URL -a).
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

type getOpts struct {
	audio, video, sponsor, playlist, sel, noFlatten, keepMeta, verbose bool
	extract, noExtract                                                 bool
	format, quality, container, subs, items, template                  string
	maxDown, maxUp, out, engine                                        string
}

func get(args []string) int {
	var o getOpts
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	for _, n := range []string{"a", "audio"} {
		fs.BoolVar(&o.audio, n, false, "")
	}
	for _, n := range []string{"v", "video"} {
		fs.BoolVar(&o.video, n, false, "")
	}
	for _, n := range []string{"o", "out"} {
		fs.StringVar(&o.out, n, "", "")
	}
	fs.BoolVar(&o.sponsor, "sponsorblock", false, "")
	fs.BoolVar(&o.playlist, "playlist", false, "")
	fs.BoolVar(&o.sel, "select", false, "")
	fs.BoolVar(&o.noFlatten, "no-flatten", false, "")
	fs.BoolVar(&o.keepMeta, "keep-meta", false, "")
	fs.BoolVar(&o.verbose, "verbose", false, "")
	fs.BoolVar(&o.extract, "extract", false, "")
	fs.BoolVar(&o.noExtract, "no-extract", false, "")
	fs.StringVar(&o.format, "format", "", "")
	fs.StringVar(&o.quality, "quality", "", "")
	fs.StringVar(&o.container, "container", "", "")
	fs.StringVar(&o.subs, "subs", "", "")
	fs.StringVar(&o.items, "items", "", "")
	fs.StringVar(&o.template, "template", "", "")
	fs.StringVar(&o.maxDown, "max-down", "", "")
	fs.StringVar(&o.maxUp, "max-up", "", "")
	fs.StringVar(&o.engine, "engine", "", "")
	inputs, err := parse(fs, args)
	if err != nil {
		return 2
	}
	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, "down: nothing to download")
		return 2
	}
	if o.audio && o.video {
		fmt.Fprintln(os.Stderr, "down: --audio and --video together make no sense")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "down:", err)
		return 1
	}
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&cfg.AudioFormat, o.format)
	set(&cfg.VideoQuality, o.quality)
	set(&cfg.Container, o.container)
	set(&cfg.Subs, o.subs)
	set(&cfg.Template, o.template)
	set(&cfg.MaxDown, o.maxDown)
	set(&cfg.MaxUp, o.maxUp)
	if o.sponsor {
		cfg.SponsorBlock = true
	}
	if o.noFlatten {
		cfg.Flatten = false
	}
	if _, err := ytdl.Height(cfg.VideoQuality); err != nil {
		fmt.Fprintln(os.Stderr, "down:", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := &runner{o: o, cfg: cfg, line: ui.NewLine()}
	defer r.close()

	failed := 0
	for _, raw := range inputs {
		in, err := detect.Parse(raw)
		if err == nil && o.engine != "" {
			switch k := detect.Kind(o.engine); k {
			case detect.Torrent, detect.Media, detect.HTTP:
				in.Kind, in.Guessed = k, false
			default:
				err = fmt.Errorf("unknown engine %q (torrent, media or http)", o.engine)
			}
		}
		if err == nil {
			pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			in = detect.Probe(pctx, &http.Client{}, in)
			cancel()
			err = r.download(ctx, in)
		}
		r.line.Clear()
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "interrupted — run the same command again to resume")
			return 130
		}
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", raw, err)
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

type runner struct {
	o    getOpts
	cfg  config.Config
	line *ui.Line
	ar   *aria.Client
}

func (r *runner) close() {
	if r.ar != nil {
		r.ar.Close()
	}
}

func (r *runner) aria() (*aria.Client, error) {
	if r.ar != nil {
		return r.ar, nil
	}
	c, err := aria.Start(aria.Options{
		CacheDir: config.CacheDir(), Trackers: r.cfg.Trackers, Connections: r.cfg.Connections,
		MaxDown: r.cfg.MaxDown, MaxUp: r.cfg.MaxUp,
	})
	r.ar = c
	return c, err
}

// outDir is -o, else the current directory.
func (r *runner) outDir() (string, error) {
	return filepath.Abs(config.Expand(r.o.out))
}

// summary is the closing line of one download.
type summary struct {
	name      string
	size      int64
	start     time.Time
	published []string
	removed   int
	freed     int64
}

func (r *runner) report(s summary) {
	where := ""
	switch len(s.published) {
	case 0:
	case 1:
		where = " → " + tilde(s.published[0])
	default:
		where = fmt.Sprintf(" → %s (%d items)", tilde(filepath.Dir(s.published[0])), len(s.published))
	}
	msg := fmt.Sprintf("✓ %s  %s in %s%s", s.name, ui.Bytes(s.size), ui.Duration(time.Since(s.start)), where)
	if s.removed > 0 {
		msg += fmt.Sprintf(" · cleaned %d files (%s)", s.removed, ui.Bytes(s.freed))
	}
	r.line.Info("%s", msg)
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

func (r *runner) download(ctx context.Context, in detect.Input) error {
	switch in.Kind {
	case detect.Media:
		return r.media(ctx, in)
	case detect.Torrent:
		return r.torrent(ctx, in)
	default:
		return r.http(ctx, in)
	}
}

func (r *runner) staging(out, id string) (string, error) {
	s := filepath.Join(out, clean.StagingDir, id)
	return s, os.MkdirAll(s, 0o755)
}

func (r *runner) media(ctx context.Context, in detect.Input) error {
	audio := r.o.audio || (in.AudioFirst && !r.o.video)
	mode := "video:" + r.cfg.VideoQuality + ":" + r.cfg.Container
	if audio {
		mode = "audio:" + r.cfg.AudioFormat
	}
	out, err := r.outDir()
	if err != nil {
		return err
	}
	staging, err := r.staging(out, in.ID(mode+":"+r.o.items+":"+strconv.FormatBool(r.o.playlist)))
	if err != nil {
		return err
	}
	var verbose io.Writer
	if r.o.verbose {
		verbose = lineWriter{r.line}
	}
	start := time.Now()
	files, runErr := ytdl.Run(ctx, in.Raw, ytdl.Options{
		Audio: audio, AudioFormat: r.cfg.AudioFormat, Quality: r.cfg.VideoQuality,
		Container: r.cfg.Container, Subs: r.cfg.Subs, SponsorBlock: r.cfg.SponsorBlock,
		Playlist: r.o.playlist, Items: r.o.items, Template: r.cfg.Template,
		Dir: staging, Verbose: r.o.verbose,
	}, func(p ytdl.Progress) {
		prefix := ""
		if p.Items > 1 {
			prefix = fmt.Sprintf("[%d/%d] ", p.Item, p.Items)
		}
		title := ui.Truncate(p.Title, 48)
		if p.PostProcess != "" {
			r.line.Update(prefix + title + "  " + postLabel(p.PostProcess) + "…")
			return
		}
		r.line.Update(prefix + title + "  " + ui.Progress(p.Done, p.Total, p.Speed))
	}, verbose)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(files) == 0 {
		if runErr == nil {
			runErr = errors.New("yt-dlp finished without a file (already in the archive, or nothing matched)")
		}
		return runErr
	}
	var rels []string
	if r.o.keepMeta {
		rels, _ = clean.Payload(staging, true)
	} else {
		for _, f := range files {
			rel, err := filepath.Rel(staging, f)
			if err != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("yt-dlp wrote outside its folder: %s", f)
			}
			rels = append(rels, rel)
		}
	}
	return r.finish(staging, out, rels, false, nil, start, mediaName(files), runErr)
}

func mediaName(files []string) string {
	if len(files) == 1 {
		return filepath.Base(files[0])
	}
	return fmt.Sprintf("%d files", len(files))
}

func postLabel(pp string) string {
	switch pp {
	case "ExtractAudio":
		return "converting audio"
	case "Merger":
		return "merging"
	case "EmbedThumbnail":
		return "embedding thumbnail"
	case "Metadata", "FFmpegMetadata":
		return "writing metadata"
	case "EmbedSubtitle":
		return "embedding subtitles"
	case "SponsorBlock", "ModifyChapters":
		return "cutting sponsors"
	case "VideoRemuxer", "VideoConvertor":
		return "remuxing"
	case "MoveFiles":
		return "moving"
	}
	return strings.ToLower(pp)
}

// finish publishes rels from staging into out, removes the staging dir and
// prints the summary; acts are files cleanup already removed.
func (r *runner) finish(staging, out string, rels []string, flat bool, acts []clean.Action, start time.Time, name string, err error) error {
	published, perr := clean.Publish(staging, out, rels, flat)
	if perr != nil {
		return fmt.Errorf("moving into %s: %w (the download is in %s)", out, perr, staging)
	}
	n, freed := clean.Leftovers(staging)
	for _, a := range acts {
		n++
		freed += a.Size
	}
	if rerr := clean.RemoveStaging(staging); rerr != nil {
		r.line.Info("! could not remove %s: %v", staging, rerr)
	}
	var size int64
	for _, p := range published {
		size += dirSize(p)
	}
	r.report(summary{name: name, size: size, start: start, published: published, removed: n, freed: freed})
	return err
}

func dirSize(p string) int64 {
	var n int64
	filepath.Walk(p, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

func (r *runner) ariaProgress(in detect.Input) func(aria.Progress) {
	return func(p aria.Progress) {
		name := p.Name
		if name == "" {
			name = in.Display()
		}
		name = ui.Truncate(name, 48)
		if p.Metadata {
			r.line.Update(fmt.Sprintf("%s  fetching metadata · %d peers", name, p.Peers))
			return
		}
		s := name + "  " + ui.Progress(p.Done, p.Total, p.Speed)
		if p.Torrent {
			s += fmt.Sprintf(" · %d peers (%d seeds)", p.Peers, p.Seeds)
		}
		r.line.Update(s)
	}
}

func (r *runner) torrent(ctx context.Context, in detect.Input) error {
	ar, err := r.aria()
	if err != nil {
		return err
	}
	out, err := r.outDir()
	if err != nil {
		return err
	}
	staging, err := r.staging(out, in.ID(""))
	if err != nil {
		return err
	}
	req := aria.Request{Dir: staging, Progress: r.ariaProgress(in), Select: r.selector(staging)}
	if in.Path != "" {
		if req.Torrent, err = os.ReadFile(in.Path); err != nil {
			return err
		}
	} else {
		req.URI = in.Raw
	}
	start := time.Now()
	res, err := ar.Download(ctx, req)
	if err != nil {
		return err
	}
	r.line.Clear()
	extract := (r.cfg.Extract || r.o.extract) && !r.o.noExtract
	var acts []clean.Action
	if !r.o.keepMeta {
		var terr error
		acts, terr = clean.TidyTorrent(staging, clean.Options{Junk: r.cfg.Junk, Extract: extract})
		if terr != nil {
			r.line.Info("! %v", terr)
		}
	}
	return r.publish(staging, out, acts, start, res.Name)
}

func (r *runner) publish(staging, out string, acts []clean.Action, start time.Time, name string) error {
	rels, err := clean.Payload(staging, r.o.keepMeta)
	if err != nil {
		return err
	}
	if len(rels) == 0 {
		return errors.New("nothing left after cleanup (everything matched the junk patterns?)")
	}
	flat := false
	if r.cfg.Flatten && !r.o.keepMeta && len(rels) == 1 {
		if f := clean.Flat(staging, rels[0]); f != "" {
			rels, flat = []string{f}, true
		}
	}
	return r.finish(staging, out, rels, flat, acts, start, name, nil)
}

func (r *runner) http(ctx context.Context, in detect.Input) error {
	ar, err := r.aria()
	if err != nil {
		return err
	}
	out, err := r.outDir()
	if err != nil {
		return err
	}
	staging, err := r.staging(out, in.ID(""))
	if err != nil {
		return err
	}
	start := time.Now()
	res, err := ar.Download(ctx, aria.Request{URI: in.Raw, Dir: staging, Progress: r.ariaProgress(in)})
	if err != nil {
		return err
	}
	r.line.Clear()
	var acts []clean.Action
	if r.o.extract && !r.o.noExtract && !r.o.keepMeta {
		var terr error
		acts, terr = clean.Tidy(staging, clean.Options{Extract: true})
		if terr != nil {
			r.line.Info("! %v", terr)
		}
		// The archive is gone; its contents are the payload, not cleanup.
		var kept []clean.Action
		for _, a := range acts {
			if a.Kind != "extract" {
				kept = append(kept, a)
			}
		}
		acts = kept
	}
	return r.publish(staging, out, acts, start, res.Name)
}

// selector picks a torrent's files: everything but junk, or, with --select,
// what the user chooses from the list.
func (r *runner) selector(staging string) func(string, []aria.File) ([]int, error) {
	return func(name string, files []aria.File) ([]int, error) {
		rels := make([]string, len(files))
		var keep, all []int
		for i, f := range files {
			rel, _ := filepath.Rel(staging, f.Path)
			rels[i] = rel
			all = append(all, f.Index)
			// The torrent's own folder name doesn't make its files junk.
			inner := rel
			if _, after, ok := strings.Cut(filepath.ToSlash(rel), "/"); ok {
				inner = after
			}
			if r.o.keepMeta || !clean.IsJunk(inner, r.cfg.Junk) {
				keep = append(keep, f.Index)
			}
		}
		if len(keep) == 0 {
			keep = all
		}
		if !r.o.sel {
			return keep, nil
		}
		r.line.Clear()
		fmt.Fprintf(os.Stderr, "%s — %d files\n", name, len(files))
		isKept := map[int]bool{}
		for _, k := range keep {
			isKept[k] = true
		}
		for i, f := range files {
			mark := ""
			if !isKept[f.Index] {
				mark = "  (junk)"
			}
			fmt.Fprintf(os.Stderr, "  %3d  %10s  %s%s\n", f.Index, ui.Bytes(f.Length), rels[i], mark)
		}
		def := "all but junk"
		if len(keep) == len(all) {
			def = "all"
		}
		for {
			a, err := ui.Ask(fmt.Sprintf("Files to download (e.g. 1,3-5 or all) [%s]: ", def))
			if err != nil {
				return nil, err
			}
			if a == "" {
				return keep, nil
			}
			if strings.EqualFold(a, "all") {
				return all, nil
			}
			idx, err := ranges(a, len(files))
			if err == nil {
				return idx, nil
			}
			fmt.Fprintln(os.Stderr, " ", err)
		}
	}
}

// ranges parses "1,3-5" into indexes within 1..n.
func ranges(s string, n int) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b > n || a > b {
			return nil, fmt.Errorf("%q is not a file number or range within 1-%d", part, n)
		}
		for i := a; i <= b; i++ {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("pick at least one file")
	}
	return out, nil
}

// lineWriter prints tool output above the status line.
type lineWriter struct{ l *ui.Line }

func (w lineWriter) Write(p []byte) (int, error) {
	w.l.Info("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
