package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/immich"
	"github.com/ashishbhatiya18/home/internal/notify"
)

func immichStatePath() string { return filepath.Join(homecfg.StateDir(), "immich-videos.json") }

// immichLock keeps a manual `home immich compress` and the job apart.
func immichLock() (func(), error) {
	p := filepath.Join(homecfg.StateDir(), "immich-videos.lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("videos are already being treated (manual run or the background job)")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func (a *app) immichRunner(logf func(string, ...any)) (*immich.Runner, error) {
	if a.cfg.Immich == nil {
		return nil, fmt.Errorf("no immich: section in %s (see the README)", homecfg.DefaultPath())
	}
	return &immich.Runner{R: a.r, Cfg: a.cfg.Immich.Resolved(), StatePath: immichStatePath(), Logf: logf}, nil
}

func (a *app) immichCmd(ctx context.Context, args []string) error {
	const usage = "usage: home immich compress [--dry-run] [--limit N] | home immich status"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "compress":
		fs := flag.NewFlagSet("immich compress", flag.ExitOnError)
		dry := fs.Bool("dry-run", false, "list the videos that would be treated, change nothing")
		limit := fs.Int("limit", 0, "treat at most N videos (0: all)")
		fs.Parse(args[1:])
		x, err := a.immichRunner(logf)
		if err != nil {
			return err
		}
		unlock, err := immichLock()
		if err != nil {
			return err
		}
		defer unlock()
		sum, err := x.Run(ctx, *limit, *dry)
		if err != nil {
			return err
		}
		if !*dry {
			fmt.Println(sum)
		}
		return nil
	case "status":
		return immichStatus()
	}
	return errors.New(usage)
}

func immichStatus() error {
	st := immich.LoadState(immichStatePath())
	var done int
	var saved int64
	notes := map[string]int{}
	var failed []string
	for _, e := range st {
		switch e.Result {
		case "done":
			done++
			saved += e.From - e.To
		case "skipped":
			n := e.Note
			if strings.HasSuffix(n, " Mbps") {
				n = "already small"
			} else if strings.HasPrefix(n, "re-encode saves only") {
				n = "re-encode would save too little"
			}
			notes[n]++
		default:
			failed = append(failed, e.At.Format("2006-01-02")+"  "+e.Path+": "+e.Note)
		}
	}
	fmt.Printf("treated: %d videos, saved %s\n", done, immich.Human(saved))
	for n, c := range notes {
		fmt.Printf("skipped: %d — %s\n", c, n)
	}
	sort.Strings(failed)
	if len(failed) > 0 {
		fmt.Printf("failed: %d (not retried; remove the entry from %s to retry)\n", len(failed), immichStatePath())
		for _, f := range failed {
			fmt.Println("  " + f)
		}
	}
	if j, ok := loadJobs()["videos"]; ok {
		fmt.Printf("last job run: %s  %s\n", j.Last.Format("2006-01-02 15:04"), j.Result)
	}
	return nil
}

// videosJob treats every untreated video once a day after immich.run_at
// (missed runs catch up), only while the Mac is on AC power: encoding keeps
// the CPU busy, so unplugging stops the run (the current encode too) and the
// hourly service resumes it once the Mac is charging again.
func (a *app) videosJob(ctx context.Context, force bool) error {
	if a.cfg.Immich == nil {
		return nil
	}
	x, _ := a.immichRunner(log.Printf)
	x.Pause = func() string {
		if onBattery() {
			return "Mac on battery"
		}
		return ""
	}
	due := homecfg.Checks{DailyAt: x.Cfg.RunAt}.DueSince(time.Now())
	if !force && !loadJobs()["videos"].Last.Before(due) {
		return nil
	}
	if !force && onBattery() {
		return nil // try again next run
	}
	unlock, err := immichLock()
	if err != nil {
		return nil // a manual run is going
	}
	defer unlock()
	if !a.r.Reachable(ctx, x.Cfg.DBNode) || !a.r.Reachable(ctx, x.Cfg.FilesNode) {
		return nil
	}
	sum, err := x.Run(ctx, 0, false)
	if err != nil {
		notify.Send("home: video job failed", err.Error())
		saveJob("videos", "failed: "+err.Error())
		return err
	}
	log.Printf("videos: %s", sum)
	if sum.Resume {
		return nil // not a full run: still due, resumes next hour
	}
	if sum.Done > 0 || len(sum.Failed) > 0 || sum.Stopped != "" {
		notify.Send("home: Immich videos", sum.String())
	}
	saveJob("videos", sum.String())
	return nil
}

func onBattery() bool {
	out, err := exec.Command("pmset", "-g", "ps").Output()
	return err == nil && strings.Contains(string(out), "'Battery Power'")
}
