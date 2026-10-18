package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ashishbhatiya18/down/internal/clean"
	"github.com/ashishbhatiya18/down/internal/config"
	"github.com/ashishbhatiya18/down/internal/ui"
)

const installScript = "curl -fsSL https://raw.githubusercontent.com/ashishbhatiya18/homelab/main/down-cli/install.sh | sh"

// fix is how to install a missing tool here: Homebrew on macOS, the
// install script (which installs every tool) elsewhere.
func fix(brew string) string {
	if runtime.GOOS == "darwin" {
		return "brew install " + brew
	}
	return "re-run the installer: " + installScript
}

// doctor checks the tools down drives; it exits 1 if one is missing.
func doctor() int {
	bad := 0
	tool := func(bin, flag, purpose, fix string) string {
		p, err := exec.LookPath(bin)
		if err != nil {
			fmt.Printf("✗ %-8s not found — %s (needed for %s)\n", bin, fix, purpose)
			bad++
			return ""
		}
		out, _ := exec.Command(p, flag).Output()
		v := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		fmt.Printf("✓ %-8s %s\n", bin, v)
		return v
	}
	if v := tool("yt-dlp", "--version", "YouTube and media sites", fix("yt-dlp")); v != "" {
		if t, err := time.Parse("2006.01.02", strings.SplitN(v, "-", 2)[0]); err == nil && time.Since(t) > 60*24*time.Hour {
			up := "brew upgrade yt-dlp"
			if runtime.GOOS != "darwin" {
				up = "sudo yt-dlp -U"
			}
			fmt.Printf("! yt-dlp is %d days old; sites change often — %s\n", int(time.Since(t).Hours()/24), up)
		}
	}
	if _, err := exec.LookPath("deno"); err != nil {
		fmt.Printf("! deno     not found — %s (yt-dlp needs a JS runtime for YouTube)\n", fix("deno"))
	}
	tool("ffmpeg", "-version", "audio extraction and merging", fix("ffmpeg"))
	tool("aria2c", "--version", "torrents and direct downloads", fix("aria2"))
	if name, _ := clean.Extractor(); name == "" {
		fmt.Printf("! no archive tool — %s (needed to extract rar/7z)\n", fix("sevenzip"))
	} else if name == "bsdtar" {
		fmt.Printf("✓ bsdtar   extracts zip/rar/7z; for multi-part RAR: %s\n", fix("sevenzip"))
	} else {
		fmt.Printf("✓ %-8s extracts archives\n", name)
	}

	_, err := config.Load()
	if err != nil {
		fmt.Printf("✗ config   %v\n", err)
		return 1
	}
	if _, err := os.Stat(config.Path()); err == nil {
		fmt.Printf("✓ config   %s\n", tilde(config.Path()))
	} else {
		fmt.Printf("✓ config   defaults (down config --init writes %s)\n", tilde(config.Path()))
	}
	if wd, err := os.Getwd(); err != nil || writable(wd) != nil {
		fmt.Printf("✗ here     the current directory is not writable (downloads land here)\n")
		bad++
	}
	if bad > 0 {
		return 1
	}
	return 0
}

// writable checks that dir, or its nearest existing parent, accepts files.
func writable(dir string) error {
	for p := dir; ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err == nil {
			if !fi.IsDir() {
				return errors.New("not a directory")
			}
			f, err := os.CreateTemp(p, ".down-check-*")
			if err != nil {
				return err
			}
			f.Close()
			return os.Remove(f.Name())
		}
		if p == filepath.Dir(p) {
			return err
		}
	}
}

func configCmd(args []string) error {
	fs := flag.NewFlagSet("down config", flag.ContinueOnError)
	initFile := fs.Bool("init", false, "write the defaults to the config file")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *initFile {
		p, err := config.Init()
		if err == nil {
			fmt.Println("wrote", tilde(p))
		}
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := os.Stat(config.Path()); err == nil {
		fmt.Printf("# %s\n", tilde(config.Path()))
	} else {
		fmt.Printf("# defaults — %s does not exist (down config --init)\n", tilde(config.Path()))
	}
	fmt.Print(cfg.YAML())
	return nil
}

func cleanCmd(args []string) error {
	fs := flag.NewFlagSet("down clean", flag.ContinueOnError)
	partial := fs.Bool("partial", false, "also remove unfinished downloads (.down/, *.part, *.aria2, …)")
	noExtract := fs.Bool("no-extract", false, "leave archives alone")
	yes := fs.Bool("yes", false, "don't ask")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: down clean <dir> [--partial] [--no-extract] [--yes]")
	}
	dir, err := filepath.Abs(config.Expand(pos[0]))
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if home, _ := os.UserHomeDir(); dir == home || dir == "/" {
		return errors.New("pick a download folder, not your home or root directory")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	o := clean.Options{Junk: cfg.Junk, Partial: *partial, Extract: !*noExtract, DryRun: true}
	plan, err := clean.Tidy(dir, o)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		fmt.Println("nothing to clean in", tilde(dir))
		return nil
	}
	var total int64
	extracts := 0
	for _, a := range plan {
		rel, _ := filepath.Rel(dir, a.Path)
		verb := map[string]string{"junk": "remove", "partial": "remove", "staging": "remove", "extract": "extract"}[a.Kind]
		fmt.Printf("  %-7s %-8s %10s  %s\n", verb, a.Kind, ui.Bytes(a.Size), rel)
		if a.Kind == "extract" {
			extracts++
		} else {
			total += a.Size
		}
	}
	q := fmt.Sprintf("Remove %d items (%s)", len(plan)-extracts, ui.Bytes(total))
	if extracts > 0 {
		q += fmt.Sprintf(" and extract %d archives (then delete them)", extracts)
	}
	if !*yes && !ui.Confirm(q+"?") {
		return errors.New("nothing changed")
	}
	o.DryRun = false
	done, err := clean.Tidy(dir, o)
	fmt.Printf("cleaned %d items in %s\n", len(done), tilde(dir))
	return err
}
