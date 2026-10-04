package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/ashishbhatiya18/hbr/internal/archive"
	"github.com/ashishbhatiya18/hbr/internal/keys"
	"github.com/ashishbhatiya18/hbr/internal/manifest"
	"github.com/ashishbhatiya18/hbr/internal/notify"
	"github.com/ashishbhatiya18/hbr/internal/retention"
	"github.com/ashishbhatiya18/hbr/internal/state"
	"github.com/ashishbhatiya18/hbr/internal/store"
)

var ErrUnreachable = errors.New("host unreachable")

// Backup takes one encrypted backup of app. Plaintext only ever exists in a
// private temp directory that is removed before returning.
func (e *Engine) Backup(ctx context.Context, app, tag string) (store.Backup, error) {
	srcs, err := e.sources(app)
	if err != nil {
		return store.Backup{}, err
	}
	recips, err := keys.Recipients(e.Cfg.KeysDir)
	if err != nil {
		return store.Backup{}, err
	}
	checked := map[string]bool{}
	for _, s := range srcs {
		if s.Host() != "" && !checked[s.Host()] {
			if !e.Env.Remote.Reachable(ctx, s.Host()) {
				return store.Backup{}, fmt.Errorf("%w: %s", ErrUnreachable, s.Host())
			}
			checked[s.Host()] = true
		}
	}

	stage, err := os.MkdirTemp("", "hbr-stage-*")
	if err != nil {
		return store.Backup{}, err
	}
	defer os.RemoveAll(stage)

	m := manifest.Manifest{App: app, CreatedAt: time.Now().UTC(), Tag: tag, ToolVersion: e.Version}
	for _, s := range srcs {
		dir := filepath.Join(stage, s.Name())
		if err := os.Mkdir(dir, 0o700); err != nil {
			return store.Backup{}, err
		}
		art, err := s.Collect(ctx, e.Env, dir)
		if err != nil {
			return store.Backup{}, fmt.Errorf("%s/%s: %w", app, s.Name(), err)
		}
		m.Artifacts = append(m.Artifacts, art)
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return store.Backup{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, manifest.FileName), mb, 0o600); err != nil {
		return store.Backup{}, err
	}

	store.CleanPartials(e.Cfg.Destination, app)
	b, err := store.Write(e.Cfg.Destination, app, m.CreatedAt, tag, func(w io.Writer) error {
		aw, err := age.Encrypt(w, recips...)
		if err != nil {
			return err
		}
		if err := archive.Write(aw, stage); err != nil {
			return err
		}
		return aw.Close()
	})
	b.Rows = totalRows(m)
	return b, err
}

func totalRows(m manifest.Manifest) int64 {
	var n int64
	for _, a := range m.Artifacts {
		if c, ok := a.Meta["row_counts"].(map[string]int64); ok {
			for _, v := range c {
				n += v
			}
		}
	}
	return n
}

// Run is the scheduled entry point: back up every app whose daily backup is
// due (no success since the last scheduled time, or all apps if force),
// prune, and raise alerts. Missed runs (asleep, offline) catch up here.
func (e *Engine) Run(ctx context.Context, force bool) error {
	now := time.Now()
	due := e.Cfg.Schedule.DueSince(now)
	var failures int
	for _, a := range e.Cfg.Apps {
		st := e.State.App(a.Name)
		if !force && !st.LastSuccess.Before(due) {
			continue
		}
		st.LastAttempt = now
		b, err := e.Backup(ctx, a.Name, "")
		if err != nil {
			if !errors.Is(err, ErrUnreachable) {
				failures++
			}
			e.Log.Printf("%s: backup could not run: %v (will retry)", a.Name, err)
			e.warn(a.Name, st, err, now)
			continue
		}
		recovered := st.LastError != ""
		st.LastSuccess, st.LastError, st.LastBackup = now, "", filepath.Base(b.Path)
		if e.Cfg.Alerts.SuccessNotifications() || recovered {
			msg := human(b.Size)
			if rows := b.Rows; rows > 0 {
				msg += fmt.Sprintf(" · %s rows", thousands(rows))
			}
			if recovered {
				msg += " · earlier problem resolved"
			}
			notify.Send("hbr: "+a.Name+" backed up ✓", msg)
		}
		e.Log.Printf("%s: backed up %s (%s)", a.Name, filepath.Base(b.Path), human(b.Size))
		if _, err := e.Prune(a.Name, false); err != nil {
			e.Log.Printf("%s: prune failed: %v", a.Name, err)
			notify.Send("hbr: "+a.Name+" cleanup failed", err.Error())
		}
	}
	e.alerts(now)
	if err := e.State.Save(); err != nil {
		return err
	}
	if failures > 0 {
		return fmt.Errorf("%d app(s) failed", failures)
	}
	return nil
}

// warn notifies that a due backup could not run. It fires immediately the
// first time, again when the reason changes, and otherwise every
// alerts.repeat_every while the problem persists (the service retries every
// few minutes, so this avoids a flood).
func (e *Engine) warn(app string, st *state.App, err error, now time.Time) {
	msg := err.Error()
	if errors.Is(err, ErrUnreachable) {
		msg = "Server unreachable (" + strings.TrimPrefix(msg, ErrUnreachable.Error()+": ") + "). On the right network? hbr keeps retrying."
	}
	if msg != st.LastError || now.Sub(st.LastAlert) > e.Cfg.Alerts.RepeatEvery.Duration {
		notify.Send("hbr: "+app+" backup could not run", msg)
		st.LastAlert = now
	}
	st.LastError = msg
}

func (e *Engine) alerts(now time.Time) {
	al := e.Cfg.Alerts
	var oldestVerify time.Time
	for _, a := range e.Cfg.Apps {
		st := e.State.App(a.Name)
		if !st.LastSuccess.IsZero() && now.Sub(st.LastSuccess) > al.StaleAfter.Duration && now.Sub(st.LastAlert) > al.RepeatEvery.Duration {
			notify.Send("hbr: "+a.Name+" is stale",
				fmt.Sprintf("No backup since %s. Is the homelab reachable?", st.LastSuccess.Format("Jan 2 15:04")))
			st.LastAlert = now
		}
		if oldestVerify.IsZero() || st.LastVerify.Before(oldestVerify) {
			oldestVerify = st.LastVerify
		}
	}
	if now.Sub(oldestVerify) > al.DrillEvery.Duration && now.Sub(e.State.LastDrillNudge) > 24*time.Hour {
		notify.Send("hbr: restore drill due", "Run `hbr verify` to prove the backups restore.")
		e.State.LastDrillNudge = now
	}
}

// Prune applies the retention policy to scheduled backups. Tagged backups
// (e.g. pre-restore safety copies) are never pruned automatically. Nothing is
// removed unless the newest backup verifies.
func (e *Engine) Prune(app string, dryRun bool) ([]store.Backup, error) {
	all, err := store.List(e.Cfg.Destination, app)
	if err != nil {
		return nil, err
	}
	var sched []store.Backup
	for _, b := range all {
		if b.Tag == "" {
			sched = append(sched, b)
		}
	}
	if len(sched) == 0 {
		return nil, nil
	}
	if err := store.VerifyChecksum(sched[0]); err != nil {
		return nil, fmt.Errorf("newest backup failed its checksum, not pruning: %w", err)
	}
	times := make([]time.Time, len(sched))
	for i, b := range sched {
		times[i] = b.Time
	}
	r := e.Cfg.Retention
	keep := retention.Keep(times, retention.Policy{Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly, MinKeep: r.MinKeep}, time.Local)
	var removed []store.Backup
	for i, b := range sched {
		if keep[i] {
			continue
		}
		removed = append(removed, b)
		if !dryRun {
			if err := store.Remove(b); err != nil {
				return removed, err
			}
			e.Log.Printf("%s: pruned %s", app, filepath.Base(b.Path))
		}
	}
	return removed, nil
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
