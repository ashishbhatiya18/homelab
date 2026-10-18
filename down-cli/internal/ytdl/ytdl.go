// Package ytdl runs yt-dlp for media pages (YouTube and the other sites it
// supports) with machine-readable progress, and reports the final files.
package ytdl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Audio        bool
	AudioFormat  string // mp3, m4a, opus, flac, best
	Quality      string // 1080p, 4k, best, …
	Container    string // mp4, mkv
	Subs         string // languages to embed
	SponsorBlock bool
	Playlist     bool   // download the whole playlist of a watch?v=…&list=… URL
	Items        string // playlist items, e.g. 1-10
	Template     string
	Dir          string
	Verbose      bool
}

const (
	tagDownload = "[down] "
	tagPost     = "[down-pp] "
	tagFile     = "[down-file] "
)

// Height turns a quality name into a maximum height; 0 means best.
func Height(q string) (int, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	switch q {
	case "", "best", "max":
		return 0, nil
	case "4k", "uhd":
		return 2160, nil
	case "2k":
		return 1440, nil
	case "hd", "fhd":
		return 1080, nil
	}
	h, err := strconv.Atoi(strings.TrimSuffix(q, "p"))
	if err != nil || h <= 0 {
		return 0, fmt.Errorf("unknown quality %q (use e.g. 720p, 1080p, 4k, best)", q)
	}
	return h, nil
}

// Args builds yt-dlp's command line.
func Args(url string, o Options) ([]string, error) {
	args := []string{
		"--quiet", "--progress", "--newline", "--color", "no_color",
		"--paths", o.Dir, "--output", o.Template,
		"--progress-template", "download:" + tagDownload +
			"%(progress.downloaded_bytes|0)s|%(progress.total_bytes,progress.total_bytes_estimate|0)s|%(progress.speed|0)s|" +
			"%(info.playlist_autonumber|)s|%(info.n_entries|)s|%(info.title)s",
		"--progress-template", "postprocess:" + tagPost + "%(progress.postprocessor)s|%(info.title)s",
		"--print", "after_move:" + tagFile + "%(filepath)s",
		"--embed-metadata", "--embed-chapters", "--embed-thumbnail",
	}
	if !o.Verbose {
		args = append(args, "--no-warnings")
	}
	if !o.Playlist {
		args = append(args, "--no-playlist")
	}
	if o.Items != "" {
		args = append(args, "--playlist-items", o.Items)
	}
	if o.SponsorBlock {
		args = append(args, "--sponsorblock-remove", "sponsor")
	}
	if o.Audio {
		args = append(args, "--format", "ba/b", "--extract-audio", "--audio-quality", "0")
		if f := o.AudioFormat; f != "" && f != "best" {
			args = append(args, "--audio-format", f)
		}
	} else {
		h, err := Height(o.Quality)
		if err != nil {
			return nil, err
		}
		sort := "res"
		if h > 0 {
			sort = "res:" + strconv.Itoa(h)
		}
		if o.Container == "mp4" {
			// Prefer codecs every player handles in mp4; resolution still wins.
			sort += ",vcodec:h264,acodec:m4a"
		}
		args = append(args, "--format", "bv*+ba/b", "--format-sort", sort)
		if o.Container != "" {
			args = append(args, "--merge-output-format", o.Container, "--remux-video", o.Container)
		}
		if o.Subs != "" {
			args = append(args, "--write-subs", "--sub-langs", o.Subs, "--embed-subs")
		}
	}
	return append(args, "--", url), nil
}

type Progress struct {
	Title       string
	Done, Total int64
	Speed       int64
	Item, Items int    // playlist position, 0 when not a playlist
	PostProcess string // set while a postprocessor runs
}

// ParseLine reads one line of yt-dlp output; ok is false for other lines.
func ParseLine(line string) (p Progress, file string, ok bool) {
	switch {
	case strings.HasPrefix(line, tagFile):
		return p, strings.TrimPrefix(line, tagFile), true
	case strings.HasPrefix(line, tagPost):
		f := strings.SplitN(strings.TrimPrefix(line, tagPost), "|", 2)
		p.PostProcess = f[0]
		if len(f) == 2 {
			p.Title = f[1]
		}
		return p, "", true
	case strings.HasPrefix(line, tagDownload):
		f := strings.SplitN(strings.TrimPrefix(line, tagDownload), "|", 6)
		if len(f) < 6 {
			return p, "", false
		}
		p.Done, p.Total, p.Speed = num(f[0]), num(f[1]), num(f[2])
		p.Item, p.Items = int(num(f[3])), int(num(f[4]))
		p.Title = f[5]
		return p, "", true
	}
	return p, "", false
}

func num(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

// Run downloads url and returns the final files (absolute paths).
func Run(ctx context.Context, url string, o Options, progress func(Progress), verbose io.Writer) ([]string, error) {
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		return nil, errors.New("yt-dlp not found (see down doctor)")
	}
	args, err := Args(url, o)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// Interrupt rather than kill, so yt-dlp keeps its .part files for resuming.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 15 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var errs []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if after, ok := strings.CutPrefix(line, "ERROR: "); ok {
				errs = append(errs, after)
			}
			if verbose != nil {
				fmt.Fprintln(verbose, line)
			}
		}
	}()
	var files []string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		p, file, ok := ParseLine(sc.Text())
		switch {
		case !ok:
			if verbose != nil {
				fmt.Fprintln(verbose, sc.Text())
			}
		case file != "":
			files = append(files, file)
		case progress != nil:
			progress(p)
		}
	}
	<-done
	err = cmd.Wait()
	if ctx.Err() != nil {
		return files, ctx.Err()
	}
	if len(errs) > 0 {
		err = errors.New(strings.Join(errs, "; "))
	} else if err != nil {
		err = fmt.Errorf("yt-dlp: %w", err)
	}
	// A playlist where some items failed still returns what it got.
	return files, err
}
