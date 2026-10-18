// Package detect works out which engine handles an input: a torrent (magnet,
// .torrent file or URL), a media page for yt-dlp, or a plain HTTP download.
package detect

import (
	"context"
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Kind string

const (
	Torrent Kind = "torrent"
	Media   Kind = "media"
	HTTP    Kind = "http"
)

// Input is one thing to download.
type Input struct {
	Raw  string
	Kind Kind
	// Path is set for a local .torrent file (absolute).
	Path string
	// InfoHash is the lowercase hex BitTorrent info hash of a magnet link.
	InfoHash string
	// Name is a display name known before downloading (a magnet's dn=).
	Name string
	// AudioFirst is set for hosts that are mostly audio (YouTube Music,
	// SoundCloud, Bandcamp): media from them defaults to audio.
	AudioFirst bool
	// Guessed is set when the kind comes from the URL alone and Probe may
	// still refine it (an unknown http(s) URL).
	Guessed bool
}

// Media hosts yt-dlp handles; anything else is probed (see Probe).
var mediaHosts = []string{
	"youtube.com", "youtu.be", "youtube-nocookie.com", "vimeo.com",
	"dailymotion.com", "twitch.tv", "twitter.com", "x.com", "instagram.com",
	"tiktok.com", "reddit.com", "facebook.com", "fb.watch", "bilibili.com",
	"soundcloud.com", "bandcamp.com", "mixcloud.com",
}

var audioHosts = []string{"music.youtube.com", "soundcloud.com", "bandcamp.com", "mixcloud.com"}

func hostIn(host string, list []string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, h := range list {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// Parse classifies raw without touching the network.
func Parse(raw string) (Input, error) {
	in := Input{Raw: raw}
	if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		u, err := url.Parse(raw)
		if err != nil {
			return in, fmt.Errorf("bad magnet link: %w", err)
		}
		q := u.Query()
		for _, xt := range q["xt"] {
			if h, ok := strings.CutPrefix(strings.ToLower(xt), "urn:btih:"); ok {
				in.InfoHash = normalizeHash(h)
			}
		}
		if in.InfoHash == "" {
			return in, errors.New("magnet link has no urn:btih info hash")
		}
		in.Kind, in.Name = Torrent, q.Get("dn")
		return in, nil
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
		case "ftp", "sftp":
			in.Kind = HTTP
			return in, nil
		default:
			return in, fmt.Errorf("unsupported URL scheme %q", u.Scheme)
		}
		switch {
		case strings.HasSuffix(strings.ToLower(u.Path), ".torrent"):
			in.Kind = Torrent
		case hostIn(u.Host, mediaHosts):
			in.Kind, in.AudioFirst = Media, hostIn(u.Host, audioHosts)
		default:
			in.Kind, in.Guessed = HTTP, true
		}
		return in, nil
	}
	if st, err := os.Stat(raw); err == nil && !st.IsDir() {
		if !strings.EqualFold(filepath.Ext(raw), ".torrent") {
			return in, fmt.Errorf("%s: only .torrent files can be given as a path", raw)
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return in, err
		}
		in.Kind, in.Path = Torrent, abs
		return in, nil
	}
	return in, fmt.Errorf("%s: not a URL, magnet link or .torrent file", raw)
}

// normalizeHash turns a 32-character base32 info hash into hex.
func normalizeHash(h string) string {
	if len(h) == 32 {
		if b, err := base32.StdEncoding.DecodeString(strings.ToUpper(h)); err == nil {
			return hex.EncodeToString(b)
		}
	}
	return h
}

// Probe refines a guessed HTTP input: a URL that serves an HTML page is
// treated as a media page for yt-dlp; anything else (or no answer) stays a
// plain download.
func Probe(ctx context.Context, c *http.Client, in Input) Input {
	if !in.Guessed {
		return in
	}
	ct := contentType(ctx, c, http.MethodHead, in.Raw)
	if ct == "" {
		ct = contentType(ctx, c, http.MethodGet, in.Raw)
	}
	if mt, _, err := mime.ParseMediaType(ct); err == nil && (mt == "text/html" || mt == "application/xhtml+xml") {
		in.Kind = Media
	}
	return in
}

func contentType(ctx context.Context, c *http.Client, method, raw string) string {
	req, err := http.NewRequestWithContext(ctx, method, raw, nil)
	if err != nil {
		return ""
	}
	if method == http.MethodGet {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	return resp.Header.Get("Content-Type")
}

// ID is a stable name for the input's staging directory, so running the same
// command again resumes. mode separates e.g. the audio and video of one URL.
func (in Input) ID(mode string) string {
	key := in.Raw
	switch {
	case in.InfoHash != "":
		key = "btih:" + in.InfoHash
	case in.Path != "":
		key = "file:" + in.Path
	}
	sum := sha1.Sum([]byte(key + "\x00" + mode))
	return hex.EncodeToString(sum[:6])
}

// Display is a short label for progress lines before the real name is known.
func (in Input) Display() string {
	switch {
	case in.Name != "":
		return in.Name
	case in.InfoHash != "":
		return "magnet " + in.InfoHash[:min(8, len(in.InfoHash))]
	case in.Path != "":
		return filepath.Base(in.Path)
	}
	return in.Raw
}
