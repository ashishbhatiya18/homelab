package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/ashishbhatiya18/hbr/internal/config"
	"github.com/ashishbhatiya18/hbr/internal/keys"
	"github.com/ashishbhatiya18/hbr/internal/manifest"
	"github.com/ashishbhatiya18/hbr/internal/prompt"
	"github.com/ashishbhatiya18/hbr/internal/remote"
	"github.com/ashishbhatiya18/hbr/internal/source"
	"github.com/ashishbhatiya18/hbr/internal/store"
)

// Verify runs a restore drill for each app: decrypt, check checksums, and let
// every source prove itself (Postgres restores into throwaway containers).
func (e *Engine) Verify(ctx context.Context, apps []string, snapshot string, recovery bool) error {
	names, err := e.appNames(apps)
	if err != nil {
		return err
	}
	id, err := e.identity(recovery)
	if err != nil {
		return err
	}
	var failed int
	for _, app := range names {
		b, err := store.Resolve(e.Cfg.Destination, app, snapshot, time.Now())
		if err != nil {
			failed++
			e.Log.Printf("%s: %v", app, err)
			continue
		}
		if err := e.verifyOne(ctx, app, b, id); err != nil {
			failed++
			e.Log.Printf("%s: DRILL FAILED: %v", app, err)
			continue
		}
		e.State.App(app).LastVerify = time.Now()
	}
	if err := e.State.Save(); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d drill(s) failed", failed)
	}
	return nil
}

func (e *Engine) verifyOne(ctx context.Context, app string, b store.Backup, id age.Identity) error {
	dir, m, cleanup, err := e.open(b, id)
	if err != nil {
		return err
	}
	defer cleanup()
	srcs, err := e.sources(app)
	if err != nil {
		return err
	}
	byName := map[string]source.Source{}
	for _, s := range srcs {
		byName[s.Name()] = s
	}
	e.Log.Printf("%s: drilling %s (taken %s)", app, filepath.Base(b.Path), m.CreatedAt.Local().Format("2006-01-02 15:04"))
	for _, a := range m.Artifacts {
		s, ok := byName[a.Source]
		if !ok {
			return fmt.Errorf("source %q is no longer in the config", a.Source)
		}
		v, ok := s.(source.Verifier)
		if !ok {
			e.Log.Printf("  %s: no drill available for type %s", a.Source, a.Kind)
			continue
		}
		res, err := v.Verify(ctx, e.Env, filepath.Join(dir, a.Source), a)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Source, err)
		}
		e.Log.Printf("  %s: OK, %s", a.Source, res)
	}
	return nil
}

// RestoreOptions control a restore.
type RestoreOptions struct {
	// Snapshot selects the backup: latest, yesterday, "3 days ago", a date,
	// or a file name (see store.Cutoff).
	Snapshot string
	Recovery bool
	DryRun   bool
	// TargetURL restores into any Postgres instead of the app's origin.
	TargetURL string
	// TargetPassword, if set, is used instead of prompting.
	TargetPassword string
	Create         bool
	Replace        bool
	// Yes skips the confirmations; only honoured with TargetURL, so
	// production restores always require a human at the keyboard.
	Yes bool
}

// Restore puts a backup back. Without TargetURL it restores to the app's own
// database: a safety backup is taken first, app containers are stopped, data
// is restored in one transaction, containers start, and health is checked.
// With TargetURL it restores into any server (a new instance, a staging DB).
func (e *Engine) Restore(ctx context.Context, app string, o RestoreOptions) error {
	b, err := store.Resolve(e.Cfg.Destination, app, o.Snapshot, time.Now())
	if err != nil {
		return err
	}
	id, err := e.identity(o.Recovery)
	if err != nil {
		return err
	}
	dir, m, cleanup, err := e.open(b, id)
	if err != nil {
		return err
	}
	defer cleanup()
	srcs, err := e.sources(app)
	if err != nil {
		return err
	}
	byName := map[string]source.Source{}
	for _, s := range srcs {
		byName[s.Name()] = s
	}

	fmt.Printf("\nBackup:  %s\nTaken:   %s\n", filepath.Base(b.Path), m.CreatedAt.Local().Format("Mon 2 Jan 2006 15:04"))
	for _, a := range m.Artifacts {
		fmt.Printf("Source:  %s (%s on %s) %s\n", a.Source, a.Kind, a.Host, summarize(a))
	}
	if o.TargetURL != "" {
		return e.restoreToTarget(ctx, app, b, dir, m, byName, o)
	}
	return e.restoreToOrigin(ctx, app, dir, m, byName, id, o)
}

func summarize(a manifest.Artifact) string {
	if c, ok := a.Meta["row_counts"].(map[string]any); ok {
		var rows float64
		for _, v := range c {
			f, _ := v.(float64)
			rows += f
		}
		return fmt.Sprintf("— %d tables, %.0f rows", len(c), rows)
	}
	return ""
}

func (e *Engine) restoreToTarget(ctx context.Context, app string, b store.Backup, dir string, m *manifest.Manifest,
	byName map[string]source.Source, o RestoreOptions) error {
	if len(m.Artifacts) != 1 {
		return errors.New("--to restores a single database; this backup has several sources")
	}
	a := m.Artifacts[0]
	pr, ok := byName[a.Source].(source.PortableRestorer)
	if !ok {
		return fmt.Errorf("source type %s cannot restore to an arbitrary target", a.Kind)
	}
	t := source.Target{URL: o.TargetURL, Create: o.Create, Replace: o.Replace, Password: o.TargetPassword}
	if t.Password == "" && !hasPassword(o.TargetURL) {
		p, err := prompt.Password("Target database password (empty if none): ")
		if err != nil {
			return err
		}
		t.Password = p
	}
	exists, empty, err := pr.InspectTarget(ctx, e.Env, t)
	if err != nil {
		return err
	}
	fmt.Printf("Target:  %s (exists: %v, empty: %v)\n\n", redact(o.TargetURL), exists, empty)
	if o.DryRun {
		fmt.Println("Dry run: nothing changed.")
		return nil
	}
	if !o.Yes {
		if err := confirmTwice(app); err != nil {
			return err
		}
	}
	if exists && !empty && o.Replace {
		// Keep a copy of what is about to be overwritten.
		safety, err := e.writeTargetSafety(ctx, app, pr, t)
		if err != nil {
			return fmt.Errorf("safety copy of target failed, nothing changed: %w", err)
		}
		e.Log.Printf("%s: safety copy of target saved as %s", app, filepath.Base(safety.Path))
	}
	res, err := pr.RestoreTo(ctx, e.Env, filepath.Join(dir, a.Source), a, t)
	if err != nil {
		return err
	}
	e.Log.Printf("%s: restored into %s — %s", app, redact(o.TargetURL), res)
	return nil
}

func (e *Engine) writeTargetSafety(ctx context.Context, app string, pr source.PortableRestorer, t source.Target) (store.Backup, error) {
	recips, err := keys.Recipients(e.Cfg.KeysDir)
	if err != nil {
		return store.Backup{}, err
	}
	return store.Write(e.Cfg.Destination, app, time.Now(), "targetcopy", func(w io.Writer) error {
		aw, err := age.Encrypt(w, recips...)
		if err != nil {
			return err
		}
		if err := pr.DumpTarget(ctx, e.Env, t, aw); err != nil {
			return err
		}
		return aw.Close()
	})
}

func (e *Engine) restoreToOrigin(ctx context.Context, app, dir string, m *manifest.Manifest,
	byName map[string]source.Source, id age.Identity, o RestoreOptions) error {
	cfgApp, err := e.Cfg.App(app)
	if err != nil {
		return err
	}
	var plan []manifest.Artifact
	for _, a := range m.Artifacts {
		s, ok := byName[a.Source]
		if !ok {
			return fmt.Errorf("source %q is no longer in the config", a.Source)
		}
		if s.Restorable() {
			plan = append(plan, a)
		}
	}
	if len(plan) == 0 {
		return errors.New("nothing in this backup is restorable to production")
	}
	fmt.Println("\nPlan:")
	fmt.Println("  1. take a safety backup of the current data")
	for _, s := range cfgApp.Restore.Stop {
		fmt.Printf("  2. stop %s on %s\n", strings.Join(s.Containers, ", "), s.Host)
	}
	for _, a := range plan {
		fmt.Printf("  3. restore %s (single transaction; untouched on failure)\n", a.Source)
	}
	fmt.Println("  4. start containers again, then check health; roll back automatically on failure")
	if o.DryRun {
		fmt.Println("\nDry run: nothing changed.")
		return nil
	}
	if err := confirmTwice(app); err != nil {
		return err
	}

	safety, err := e.Backup(ctx, app, "prerestore")
	if err != nil {
		return fmt.Errorf("safety backup failed, nothing changed: %w", err)
	}
	e.Log.Printf("%s: safety backup %s", app, filepath.Base(safety.Path))

	restoreErr := e.applyRestore(ctx, cfgApp.Restore.Stop, cfgApp.Restore.Health.URL, cfgApp.Restore.Health.Timeout.Duration, dir, plan, byName)
	if restoreErr == nil {
		e.Log.Printf("%s: restore complete and healthy", app)
		return nil
	}
	e.Log.Printf("%s: restore failed (%v); rolling back to %s", app, restoreErr, filepath.Base(safety.Path))
	sdir, sm, scleanup, err := e.open(safety, id)
	if err != nil {
		return fmt.Errorf("restore failed (%v) AND rollback could not open safety backup: %w", restoreErr, err)
	}
	defer scleanup()
	var splan []manifest.Artifact
	for _, a := range sm.Artifacts {
		if s, ok := byName[a.Source]; ok && s.Restorable() {
			splan = append(splan, a)
		}
	}
	if err := e.applyRestore(ctx, cfgApp.Restore.Stop, cfgApp.Restore.Health.URL, cfgApp.Restore.Health.Timeout.Duration, sdir, splan, byName); err != nil {
		return fmt.Errorf("restore failed (%v) AND rollback failed: %w — safety backup is %s", restoreErr, err, safety.Path)
	}
	return fmt.Errorf("restore failed and was rolled back to the safety backup: %w", restoreErr)
}

func (e *Engine) applyRestore(ctx context.Context, stops []config.StopSpec, healthURL string, healthTimeout time.Duration,
	dir string, plan []manifest.Artifact, byName map[string]source.Source) (err error) {
	for _, s := range stops {
		if e2 := e.containers(ctx, s.Host, "stop", s.Containers); e2 != nil {
			return e2
		}
	}
	defer func() {
		for _, s := range stops {
			if e2 := e.containers(ctx, s.Host, "start", s.Containers); e2 != nil && err == nil {
				err = e2
			}
		}
		if err == nil {
			err = e.waitRunning(ctx, stops)
		}
		if err == nil && healthURL != "" {
			err = waitHealthy(ctx, healthURL, healthTimeout)
		}
	}()
	for _, a := range plan {
		if err := byName[a.Source].Restore(ctx, e.Env, filepath.Join(dir, a.Source), a); err != nil {
			return fmt.Errorf("%s: %w", a.Source, err)
		}
	}
	return nil
}

func (e *Engine) containers(ctx context.Context, host, action string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = remote.Quote(n)
	}
	return e.Env.Remote.Run(ctx, host, "docker "+action+" "+strings.Join(q, " "), nil, nil)
}

// waitRunning checks that every restarted container is still up after a
// settling period and has not been restarting (a crash loop after restore).
func (e *Engine) waitRunning(ctx context.Context, stops []config.StopSpec) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Second):
	}
	for _, s := range stops {
		for _, c := range s.Containers {
			out, err := e.Env.Remote.Output(ctx, s.Host, "docker inspect -f '{{.State.Running}} {{.State.Restarting}}' "+remote.Quote(c))
			if err != nil {
				return err
			}
			if strings.TrimSpace(out) != "true false" {
				return fmt.Errorf("container %s on %s is not healthy after restore (%s)", c, s.Host, strings.TrimSpace(out))
			}
		}
	}
	return nil
}

func waitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 10 * time.Second}
	var last string
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil
			}
			last = resp.Status
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("health check %s did not pass within %s (last: %s)", url, timeout, last)
}

func confirmTwice(app string) error {
	if err := prompt.Confirm(fmt.Sprintf("\nType the app name (%s) to continue: ", app), app); err != nil {
		return err
	}
	return prompt.Confirm("This overwrites data. Type RESTORE to confirm: ", "RESTORE")
}

func hasPassword(u string) bool {
	creds, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(u, "postgres://"), "postgresql://"), "@")
	return ok && strings.Contains(creds, ":")
}

func redact(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return u
	}
	creds, host, ok := strings.Cut(rest, "@")
	if !ok {
		return u
	}
	user, _, _ := strings.Cut(creds, ":")
	return scheme + "://" + user + "@" + host
}
