// Package setup is the interactive first-run wizard (`hbr setup`).
//
// It discovers as much as possible instead of asking: it tests passwordless
// SSH, lists Postgres containers on the server, and lists their databases, so
// the user mostly picks from menus.
package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ashishbhatiya18/hbr/internal/config"
	"github.com/ashishbhatiya18/hbr/internal/keys"
	"github.com/ashishbhatiya18/hbr/internal/prompt"
	"github.com/ashishbhatiya18/hbr/internal/remote"
	"github.com/ashishbhatiya18/hbr/internal/secret"
	"github.com/ashishbhatiya18/hbr/internal/source"
)

type wizard struct {
	ctx    context.Context
	runner *remote.Runner
	doc    config.Doc
}

func say(format string, a ...any) { fmt.Printf(format+"\n", a...) }

// Run walks the user through creating the config at path and the keys.
func Run(ctx context.Context, path string) error {
	say("\nhbr setup — encrypted, portable Postgres backups\n")
	if _, err := os.Stat(path); err == nil {
		ok, err := prompt.YesNo("A config already exists at "+path+". Replace it (the old one is kept as .prev)?", false)
		if err != nil || !ok {
			return errors.New("setup cancelled")
		}
	}
	w := &wizard{ctx: ctx, runner: &remote.Runner{Hosts: map[string]string{}, ConnectTimeout: 6 * time.Second}}
	w.doc.Hosts = map[string]config.Host{}
	steps := []func() error{w.destination, w.schedule, w.retention, w.apps, w.verify}
	for _, s := range steps {
		if err := s(); err != nil {
			return err
		}
	}
	if err := config.Save(path, w.doc); err != nil {
		return err
	}
	say("\n✓ Config saved to %s", path)
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if !keys.Exists(cfg.KeysDir) {
		if err := InitKeys(cfg); err != nil {
			return err
		}
	} else {
		say("✓ Using existing keys in %s", cfg.KeysDir)
	}
	return nil
}

// ---- steps ------------------------------------------------------------------

func (w *wizard) destination() error {
	home, _ := os.UserHomeDir()
	def := "~/Backups/hbr"
	if st, err := os.Stat(filepath.Join(home, "Dropbox")); err == nil && st.IsDir() {
		def = "~/Dropbox/Backups"
	}
	say("Where should encrypted backups be stored? A synced folder (Dropbox, iCloud Drive)")
	say("gives you an off-site copy for free; backups are unreadable without your keys.")
	for {
		d, err := prompt.Ask("Backup folder", def)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(config.Expand(d), 0o700); err != nil {
			say("  ✗ cannot create %s: %v", d, err)
			continue
		}
		w.doc.Destination = d
		w.doc.KeysDir = filepath.ToSlash(filepath.Join(d, "keys"))
		return nil
	}
}

func (w *wizard) schedule() error {
	say("\nBackups run once a day. If the Mac is asleep or the server is unreachable at that")
	say("time, hbr catches up at the next opportunity and warns you if it can't.")
	re := regexp.MustCompile(`^([01]?\d|2[0-3]):[0-5]\d$`)
	for {
		t, err := prompt.Ask("Daily backup time (HH:MM, local)", "02:00")
		if err != nil {
			return err
		}
		if re.MatchString(t) {
			w.doc.Schedule.DailyAt = t
			return nil
		}
		say("  please use HH:MM, e.g. 02:00")
	}
}

func (w *wizard) retention() error {
	say("\nRetention: keep 7 daily, 4 weekly and 12 monthly snapshots (never fewer than 3),")
	say("so there is always a copy from yesterday and one about a month old.")
	ok, err := prompt.YesNo("Use this retention?", true)
	if err != nil {
		return err
	}
	r := config.Retention{Daily: 7, Weekly: 4, Monthly: 12, MinKeep: 3}
	if !ok {
		for _, f := range []struct {
			label string
			v     *int
		}{{"Daily snapshots", &r.Daily}, {"Weekly snapshots", &r.Weekly}, {"Monthly snapshots", &r.Monthly}} {
			s, err := prompt.Ask(f.label, fmt.Sprint(*f.v))
			if err != nil {
				return err
			}
			fmt.Sscan(s, f.v)
		}
	}
	w.doc.Retention = r
	return nil
}

func (w *wizard) apps() error {
	for {
		say("")
		i, err := prompt.Choose("Where is the Postgres database you want to back up?", []string{
			"In a Docker container on a server I can SSH into (no DB password needed)",
			"Reachable directly from this Mac (host/port, password kept in Keychain)",
		}, 0)
		if err != nil {
			return err
		}
		if i == 0 {
			err = w.dockerApps()
		} else {
			err = w.urlApp()
		}
		if err != nil {
			say("  ✗ %v", err)
		}
		more, err := prompt.YesNo("\nAdd another database?", false)
		if err != nil {
			return err
		}
		if !more {
			if len(w.doc.Apps) == 0 {
				say("At least one database is needed.")
				continue
			}
			return nil
		}
	}
}

// sshHost asks for an SSH target and proves passwordless access works.
func (w *wizard) sshHost(label string) (string, error) {
	for {
		target, err := prompt.Ask(label+" (user@host or an alias from ~/.ssh/config)", "")
		if err != nil {
			return "", err
		}
		if target == "" {
			continue
		}
		alias := hostAlias(target)
		w.runner.Hosts[alias] = target
		say("  checking passwordless SSH to %s …", target)
		if w.runner.Reachable(w.ctx, alias) {
			say("  ✓ SSH works")
			w.doc.Hosts[alias] = config.Host{SSH: target}
			return alias, nil
		}
		say("  ✗ could not log in without a password. hbr runs unattended, so it needs key-based SSH:")
		say("      ssh-keygen -t ed25519        # if you have no key yet")
		say("      ssh-copy-id %s", target)
		say("    then confirm `ssh %s true` works without prompting.", target)
		retry, err := prompt.YesNo("  Try again?", true)
		if err != nil {
			return "", err
		}
		if !retry {
			return "", errors.New("skipped: passwordless SSH not available")
		}
	}
}

func (w *wizard) dockerApps() error {
	host, err := w.sshHost("Server running Postgres")
	if err != nil {
		return err
	}
	out, err := w.runner.Output(w.ctx, host, `docker ps --format '{{.Names}}\t{{.Image}}'`)
	if err != nil {
		return fmt.Errorf("cannot run docker on that server (is the SSH user in the docker group?): %w", err)
	}
	var names, labels []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		n, img, _ := strings.Cut(line, "\t")
		if regexp.MustCompile(`(?i)postgres|postgis|timescale|supabase`).MatchString(img) {
			names = append(names, n)
			labels = append(labels, n+"  ("+img+")")
		}
	}
	var container string
	switch len(names) {
	case 0:
		if container, err = prompt.Ask("No Postgres container detected. Container name", ""); err != nil {
			return err
		}
	case 1:
		container = names[0]
		say("  ✓ found Postgres container %s", labels[0])
	default:
		i, err := prompt.Choose("Which container?", labels, 0)
		if err != nil {
			return err
		}
		container = names[i]
	}
	user, err := prompt.Ask("Postgres superuser inside the container", "postgres")
	if err != nil {
		return err
	}
	list, err := source.DockerQuery(w.ctx, w.runner, host, container, user, "postgres",
		"select datname from pg_database where not datistemplate and datname <> 'postgres' order by 1")
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}
	dbs := strings.Fields(list)
	if len(dbs) == 0 {
		return errors.New("no databases found in that container")
	}
	picked, err := prompt.ChooseMany("Databases found — which should be backed up?", dbs)
	if err != nil {
		return err
	}
	for _, i := range picked {
		db := dbs[i]
		name, err := w.appName(db)
		if err != nil {
			return err
		}
		app := config.AppDoc{Name: name, Sources: []config.SourceDoc{{
			Type: "postgres-docker", Name: "db", Host: host, Container: container, User: user, Database: db,
		}}}
		if stop, err := w.stopContainers(name); err != nil {
			return err
		} else if stop != nil {
			app.Restore = &config.RestoreDoc{Stop: stop}
		}
		w.doc.Apps = append(w.doc.Apps, app)
		say("  ✓ %s → database %s", name, db)
	}
	return nil
}

func (w *wizard) urlApp() error {
	host, err := prompt.Ask("Database host", "localhost")
	if err != nil {
		return err
	}
	port, err := prompt.Ask("Port", "5432")
	if err != nil {
		return err
	}
	user, err := prompt.Ask("User (read access to the database is enough for backups)", "postgres")
	if err != nil {
		return err
	}
	db, err := prompt.Ask("Database name", "")
	if err != nil {
		return err
	}
	ssl, err := prompt.Ask("sslmode (disable, prefer, require)", "prefer")
	if err != nil {
		return err
	}
	pw, err := prompt.Password("Password (stored in the macOS Keychain, not the config): ")
	if err != nil {
		return err
	}
	u := fmt.Sprintf("postgres://%s@%s:%s/%s?sslmode=%s", user, host, port, db, ssl)
	say("  testing connection …")
	cmd := exec.CommandContext(w.ctx, pgTool("psql"), "-X", "-tAc", "select 1", "-d", u)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+pw, "PGCONNECT_TIMEOUT=10")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("connection failed: %s", strings.TrimSpace(string(out)))
	}
	if err := secret.Set(source.KeychainAccount(u), pw); err != nil {
		return err
	}
	say("  ✓ connected; password saved to Keychain")
	name, err := w.appName(db)
	if err != nil {
		return err
	}
	w.doc.Apps = append(w.doc.Apps, config.AppDoc{Name: name, Sources: []config.SourceDoc{{Type: "postgres-url", Name: "db", URL: u}}})
	return nil
}

func (w *wizard) appName(db string) (string, error) {
	re := regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	for {
		n, err := prompt.Ask(fmt.Sprintf("  Name for %q backups", db), db)
		if err != nil {
			return "", err
		}
		taken := false
		for _, a := range w.doc.Apps {
			taken = taken || a.Name == n
		}
		if re.MatchString(n) && !taken {
			return n, nil
		}
		say("  use letters, digits, . _ - and a name not used yet")
	}
}

// stopContainers asks which app containers to stop while restoring to
// production, so the app doesn't write during the restore.
func (w *wizard) stopContainers(app string) ([]config.StopSpec, error) {
	ok, err := prompt.YesNo(fmt.Sprintf("  Should a production restore of %s stop the app's containers first?", app), true)
	if err != nil || !ok {
		return nil, err
	}
	host, err := w.sshHost("  Server running " + app + "'s app containers")
	if err != nil {
		return nil, nil // optional; keep going without
	}
	out, _ := w.runner.Output(w.ctx, host, `docker ps --format '{{.Names}}'`)
	running := strings.Fields(out)
	if len(running) == 0 {
		return nil, nil
	}
	idx, err := prompt.ChooseMany("  Containers to stop during a restore:", running)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, i := range idx {
		names = append(names, running[i])
	}
	return []config.StopSpec{{Host: host, Containers: names}}, nil
}

func (w *wizard) verify() error {
	cli := "docker"
	if _, err := exec.LookPath("docker"); err != nil {
		if _, err := exec.LookPath("podman"); err == nil {
			cli = "podman"
		}
	}
	w.doc.Verify = config.VerifyDoc{ContainerCLI: cli, PostgresMajors: []int{0, 18}}
	return nil
}

// ---- keys ---------------------------------------------------------------------

// InitKeys creates the encryption keys, shows the recovery key once and makes
// the user prove it was saved.
func InitKeys(cfg *config.Config) error {
	say("\nEncryption keys")
	say("Backups are encrypted so only you can read them. Choose a password: it is needed")
	say("to restore or run drills, never for the daily backups themselves.")
	pass, err := prompt.NewPassword("New password (12+ characters): ")
	if err != nil {
		return err
	}
	rec, err := keys.Init(cfg.KeysDir, pass)
	if err != nil {
		return err
	}
	fmt.Printf(`
==================================================================
RECOVERY KEY — shown once. It opens every backup without the password,
for example if this Mac is lost and you have forgotten the password.
Save it in a password manager you can reach from another device
(e.g. Google Password Manager: site "hbr-recovery").

%s
==================================================================
`, rec)
	if os.Getenv(prompt.PassphraseEnv) == "" {
		for {
			got, err := prompt.Password("\nPaste the recovery key back from where you saved it: ")
			if err != nil {
				return err
			}
			if id, err := keys.ParseRecovery(got); err == nil && id.String() == rec {
				break
			}
			say("That does not match — check what you saved and try again.")
		}
	}
	if _, err := keys.DailyIdentity(cfg.KeysDir, pass); err != nil {
		return fmt.Errorf("self-check failed: %w", err)
	}
	say("\n✓ Keys created in %s. Clear the terminal (⌘K) to remove the key from screen.", cfg.KeysDir)
	return nil
}

// ---- helpers --------------------------------------------------------------------

func hostAlias(target string) string {
	h := target
	if _, after, ok := strings.Cut(target, "@"); ok {
		h = after
	}
	return strings.Map(func(r rune) rune {
		if r == '.' || r == ':' {
			return '-'
		}
		return r
	}, h)
}

func pgTool(name string) string {
	for _, dir := range []string{"/opt/homebrew/opt/libpq/bin", "/usr/local/opt/libpq/bin"} {
		if p := filepath.Join(dir, name); fileExists(p) {
			return p
		}
	}
	return name
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
