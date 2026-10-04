// Command hbr (homelab-backup-restore) takes encrypted, portable Postgres
// backups on a schedule and restores them into any Postgres. See README.md.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ashishbhatiya18/hbr/internal/config"
	"github.com/ashishbhatiya18/hbr/internal/engine"
	"github.com/ashishbhatiya18/hbr/internal/keys"
	"github.com/ashishbhatiya18/hbr/internal/notify"
	"github.com/ashishbhatiya18/hbr/internal/prompt"
	"github.com/ashishbhatiya18/hbr/internal/service"
	"github.com/ashishbhatiya18/hbr/internal/setup"
	"github.com/ashishbhatiya18/hbr/internal/source"
	"github.com/ashishbhatiya18/hbr/internal/store"
)

var version = "dev"

const usage = `hbr — homelab backup & restore: encrypted, portable Postgres backups

Getting started:
  hbr setup                     interactive setup (runs automatically the first time)
  hbr install                   run backups in the background (managed by brew services)
  hbr uninstall                 stop the background service

Everyday:
  hbr status                    last backup per app, service and server status
  hbr snapshots [app]           list snapshots
  hbr backup [app...]           take a backup now
  hbr verify [app...]           restore drill into throwaway Postgres (asks password)
      --snapshot S                which snapshot (default: latest)
  hbr restore <app>             restore a snapshot (asks password, confirms twice)
      --snapshot S                latest | yesterday | "3 days ago" | "1 month ago" |
                                  2026-10-03 | "2026-10-03 14:30" | <file name>
      --to URL                    restore into any Postgres, e.g.
                                  postgres://admin@newhost:5432/mydb
      --create                    with --to: create the database if missing
      --replace                   with --to: overwrite a non-empty database
                                  (a safety copy is taken first)
      --password-stdin            with --to: read the target password from stdin
      --local                     restore into a new Postgres container on this Mac
                                  (docker/podman), left running to explore
      --pg-version N              with --local: Postgres major (default: the snapshot's)
      --yes                       with --to: skip confirmations (never for production)
      --dry-run                   check the password, snapshot and target; change nothing
      --recovery                  unlock with the recovery key instead of the password

Maintenance:
  hbr check                     validate the config
  hbr prune [--dry-run]         apply the retention policy now
  hbr passwd                    change the backup password
  hbr keys                      create keys (if setup was interrupted)
  hbr run [--force]             scheduled run (what the service calls)
  hbr version

Config: $HBR_CONFIG or ~/.config/hbr/config.yaml (override with --config).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, args := "", os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if err := dispatch(ctx, cmd, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func dispatch(ctx context.Context, cmd string, args []string) error {
	fs := flag.NewFlagSet("hbr "+cmd, flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	load := func() (*engine.Engine, error) {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return nil, err
		}
		return engine.New(cfg, version)
	}
	parse := func() { fs.Parse(reorder(args, fs)) }

	switch cmd {
	case "":
		// Vanilla start: set up if there is no config yet, otherwise show status.
		if _, err := os.Stat(*cfgPath); errors.Is(err, os.ErrNotExist) {
			return firstRun(ctx, *cfgPath)
		}
		e, err := load()
		if err != nil {
			return err
		}
		if err := status(ctx, e); err != nil {
			return err
		}
		fmt.Println("\nRun `hbr help` for all commands.")
		return nil

	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil

	case "version", "--version", "-v":
		fmt.Printf("hbr %s (source types: %s)\n", version, strings.Join(source.Types(), ", "))
		return nil

	case "setup":
		parse()
		return firstRun(ctx, *cfgPath)

	case "keys":
		parse()
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		if keys.Exists(cfg.KeysDir) {
			return fmt.Errorf("keys already exist in %s", cfg.KeysDir)
		}
		return setup.InitKeys(cfg)

	case "install":
		parse()
		e, err := load()
		if err != nil {
			return fmt.Errorf("%w\nRun `hbr setup` first", err)
		}
		if *cfgPath != config.DefaultPath() {
			return fmt.Errorf("the service reads %s; move your config there first", config.DefaultPath())
		}
		if !keys.Exists(e.Cfg.KeysDir) {
			return errors.New("no encryption keys yet; run `hbr keys`")
		}
		if err := service.Install(); err != nil {
			return err
		}
		fmt.Printf("\n✓ hbr runs in the background: daily at %s, catching up after sleep.\n", e.Cfg.Schedule.DailyAt)
		fmt.Println("  You'll get a notification after each backup, and a warning if one can't run.")
		fmt.Println("  Manage it with `brew services info|restart|stop hbr` or `hbr uninstall`.")
		return nil

	case "uninstall":
		parse()
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Service stopped. Backups, config and keys are untouched.")
		return nil

	case "check":
		parse()
		e, err := load()
		if err != nil {
			return err
		}
		fmt.Printf("config OK: %d app(s), daily at %s, backups in %s\n", len(e.Cfg.Apps), e.Cfg.Schedule.DailyAt, e.Cfg.Destination)
		return nil

	case "run":
		force := fs.Bool("force", false, "back up every app even if not due")
		parse()
		e, err := load()
		if err != nil {
			// The service has nobody watching its log: say so on screen.
			notify.Send("hbr: backups cannot run", err.Error())
			return err
		}
		return e.Run(ctx, *force)

	case "backup":
		parse()
		e, err := load()
		if err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return e.Run(ctx, true)
		}
		for _, a := range fs.Args() {
			b, err := e.Backup(ctx, a, "")
			if err != nil {
				return err
			}
			st := e.State.App(a)
			st.LastSuccess, st.LastError, st.LastBackup = time.Now(), "", filepath.Base(b.Path)
			fmt.Printf("%s: %s (%d KB)\n", a, filepath.Base(b.Path), (b.Size+1023)/1024)
			if _, err := e.Prune(a, false); err != nil {
				return err
			}
		}
		return e.State.Save()

	case "status":
		parse()
		e, err := load()
		if err != nil {
			return err
		}
		return status(ctx, e)

	case "snapshots", "list":
		parse()
		e, err := load()
		if err != nil {
			return err
		}
		return snapshots(e, fs.Args())

	case "prune":
		dry := fs.Bool("dry-run", false, "only show what would be removed")
		parse()
		e, err := load()
		if err != nil {
			return err
		}
		for _, a := range e.Cfg.Apps {
			rm, err := e.Prune(a.Name, *dry)
			if err != nil {
				return err
			}
			for _, b := range rm {
				verb := "removed"
				if *dry {
					verb = "would remove"
				}
				fmt.Printf("%s: %s %s\n", a.Name, verb, filepath.Base(b.Path))
			}
		}
		return nil

	case "verify":
		snap := fs.String("snapshot", "latest", "snapshot to drill")
		recovery := fs.Bool("recovery", false, "unlock with the recovery key")
		parse()
		e, err := load()
		if err != nil {
			return err
		}
		return e.Verify(ctx, fs.Args(), *snap, *recovery)

	case "restore":
		var o engine.RestoreOptions
		fs.StringVar(&o.Snapshot, "snapshot", "latest", "which snapshot")
		fs.StringVar(&o.TargetURL, "to", "", "restore into this Postgres URL instead of production")
		fs.BoolVar(&o.Create, "create", false, "create the target database if missing")
		fs.BoolVar(&o.Replace, "replace", false, "overwrite a non-empty target database")
		fs.BoolVar(&o.DryRun, "dry-run", false, "show the plan only")
		fs.BoolVar(&o.Recovery, "recovery", false, "unlock with the recovery key")
		fs.BoolVar(&o.Yes, "yes", false, "with --to: skip confirmations")
		fs.BoolVar(&o.Local, "local", false, "restore into a new Postgres container on this machine")
		fs.IntVar(&o.PgVersion, "pg-version", 0, "with --local: Postgres major version (default: the snapshot's)")
		pwStdin := fs.Bool("password-stdin", false, "with --to: read the target password from stdin")
		parse()
		if fs.NArg() != 1 {
			return errors.New("usage: hbr restore <app> [--snapshot S] [--to URL] …")
		}
		if o.Local && o.TargetURL != "" {
			return errors.New("use either --local or --to, not both")
		}
		if (o.Yes || *pwStdin) && o.TargetURL == "" {
			return errors.New("--yes and --password-stdin only work with --to; production restores always ask")
		}
		if *pwStdin {
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && err != io.EOF {
				return err
			}
			o.TargetPassword = strings.TrimRight(line, "\r\n")
		}
		e, err := load()
		if err != nil {
			return err
		}
		return e.Restore(ctx, fs.Arg(0), o)

	case "passwd":
		parse()
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		old, err := prompt.Password("Current password: ")
		if err != nil {
			return err
		}
		nw, err := prompt.NewPassword("New password: ")
		if err != nil {
			return err
		}
		if err := keys.ChangePassword(cfg.KeysDir, old, nw); err != nil {
			return err
		}
		fmt.Println("Password changed. Existing snapshots are unaffected.")
		return nil
	}
	return fmt.Errorf("unknown command %q — see `hbr help`", cmd)
}

func firstRun(ctx context.Context, path string) error {
	if err := setup.Run(ctx, path); err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	e, err := engine.New(cfg, version)
	if err != nil {
		return err
	}
	if ok, _ := prompt.YesNo("\nTake the first backup now?", true); ok {
		if err := e.Run(ctx, true); err != nil {
			fmt.Println("  ✗", err)
		}
		if ok, _ := prompt.YesNo("Run a restore drill now to prove the backups restore (asks your password)?", true); ok {
			if err := e.Verify(ctx, nil, "latest", false); err != nil {
				fmt.Println("  ✗", err)
			}
		}
	}
	if path == config.DefaultPath() {
		if ok, _ := prompt.YesNo("\nInstall the background service now (`hbr install`)?", true); ok {
			if err := service.Install(); err != nil {
				fmt.Println("  ✗", err)
			}
		}
	}
	fmt.Println("\nDone. `hbr status` shows how things stand; `hbr help` lists every command.")
	return nil
}

// reorder lets flags follow positional args (`hbr restore myapp --dry-run`).
func reorder(args []string, fs *flag.FlagSet) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil {
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		}
	}
	return append(flags, pos...)
}

func status(ctx context.Context, e *engine.Engine) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tLAST BACKUP\tSNAPSHOTS\tOLDEST\tLAST DRILL\tPROBLEM")
	for _, a := range e.Cfg.Apps {
		st := e.State.App(a.Name)
		bs, _ := store.List(e.Cfg.Destination, a.Name)
		oldest := "-"
		if len(bs) > 0 {
			oldest = ago(bs[len(bs)-1].Time)
		}
		problem := st.LastError
		if len(problem) > 70 {
			problem = problem[:70] + "…"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n", a.Name, ago(st.LastSuccess), len(bs), oldest, ago(st.LastVerify), problem)
	}
	w.Flush()
	fmt.Printf("\nschedule: daily at %s · backups in %s\n", e.Cfg.Schedule.DailyAt, e.Cfg.Destination)
	for name, h := range e.Cfg.Hosts {
		state := "reachable"
		if !e.Env.Remote.Reachable(ctx, name) {
			state = "NOT reachable"
		}
		fmt.Printf("server %s (%s): %s\n", name, h.SSH, state)
	}
	if !keys.Exists(e.Cfg.KeysDir) {
		fmt.Println("keys: missing — run `hbr keys`")
	}
	fmt.Println(service.Describe())
	return nil
}

func snapshots(e *engine.Engine, only []string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tTAKEN\tAGE\tSIZE\tKIND\tFILE")
	for _, a := range e.Cfg.Apps {
		if len(only) > 0 && !contains(only, a.Name) {
			continue
		}
		bs, err := store.List(e.Cfg.Destination, a.Name)
		if err != nil {
			return err
		}
		for _, b := range bs {
			kind := "scheduled"
			if b.Tag != "" {
				kind = b.Tag
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d KB\t%s\t%s\n", a.Name, b.Time.Local().Format("2006-01-02 15:04"), ago(b.Time), (b.Size+1023)/1024, kind, filepath.Base(b.Path))
		}
	}
	return w.Flush()
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
