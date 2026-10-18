// Package config holds down's settings: ~/.config/down/config.yaml over
// built-in defaults. Every field is optional in the file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Downloads always land in the current directory (or -o).
type Config struct {
	// Media (yt-dlp).
	AudioFormat  string `yaml:"audio_format"`  // mp3, m4a, opus, flac, best
	VideoQuality string `yaml:"video_quality"` // 2160p/4k, 1440p, 1080p, 720p, 480p, best
	Container    string `yaml:"container"`     // mp4, mkv
	Subs         string `yaml:"subs"`          // subtitle languages to embed, e.g. "en" or "en,hi"
	SponsorBlock bool   `yaml:"sponsorblock"`  // cut sponsor segments
	Template     string `yaml:"template"`      // yt-dlp output template

	// Torrents and HTTP (aria2).
	Connections int      `yaml:"connections"` // per HTTP server, 1–16
	MaxDown     string   `yaml:"max_down"`    // e.g. 5M; empty = unlimited
	MaxUp       string   `yaml:"max_up"`
	Trackers    []string `yaml:"trackers"` // added to every torrent

	// Cleanup.
	Extract bool     `yaml:"extract"` // unpack zip/rar/7z from torrents, then delete them
	Flatten bool     `yaml:"flatten"` // a folder holding one file becomes the file
	Junk    []string `yaml:"junk"`    // name patterns never kept from torrents (case-insensitive)
}

const DefaultTemplate = "%(playlist_title&{} - |)s%(playlist_index&{:03d} - |)s%(title)s [%(id)s].%(ext)s"

func Default() Config {
	return Config{
		AudioFormat:  "mp3",
		VideoQuality: "1080p",
		Container:    "mp4",
		Template:     DefaultTemplate,
		Connections:  16,
		Trackers: []string{
			"udp://tracker.opentrackr.org:1337/announce",
			"udp://open.demonii.com:1337/announce",
			"udp://open.stealth.si:80/announce",
			"udp://exodus.desync.com:6969/announce",
			"udp://tracker.torrent.eu.org:451/announce",
		},
		Extract: true,
		Flatten: true,
		Junk: []string{
			"*.nfo", "*.txt", "*.url", "*.lnk", "*.website", "*.sfv", "*.md5",
			"*.torrent", "*.exe", "*.scr", "*.bat",
			"sample", "samples", "*sample.*", "*-sample-*", "proof",
			"rarbg*", "www.*", "thumbs.db", "desktop.ini", ".ds_store",
		},
	}
}

// Path is $DOWN_CONFIG, else $XDG_CONFIG_HOME/down/config.yaml, else
// ~/.config/down/config.yaml.
func Path() string {
	if p := os.Getenv("DOWN_CONFIG"); p != "" {
		return p
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "down", "config.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "down", "config.yaml")
}

// CacheDir holds aria2's DHT routing table, shared by all downloads.
func CacheDir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "down")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "down")
}

// Load reads the config file over the defaults; a missing file is fine.
func Load() (Config, error) {
	c := Default()
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", Path(), err)
	}
	return c, nil
}

// Expand resolves a leading ~.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

// Init writes the defaults to the config file; it never overwrites one.
func Init() (string, error) {
	p := Path()
	if _, err := os.Stat(p); err == nil {
		return p, fmt.Errorf("%s already exists", p)
	}
	b, err := yaml.Marshal(Default())
	if err != nil {
		return p, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return p, err
	}
	head := "# down settings; every key is optional (see `down config`).\n"
	return p, os.WriteFile(p, append([]byte(head), b...), 0o644)
}

// YAML renders c for `down config`.
func (c Config) YAML() string {
	b, _ := yaml.Marshal(c)
	return string(b)
}
