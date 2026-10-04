package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/ashishbhatiya18/hbr/internal/manifest"
	"github.com/ashishbhatiya18/hbr/internal/source"
	"github.com/ashishbhatiya18/hbr/internal/store"
)

// restoreLocal starts a fresh Postgres container on this machine, restores the
// snapshot into it and leaves it running so the data can be explored.
func (e *Engine) restoreLocal(ctx context.Context, app string, b store.Backup, dir string, m *manifest.Manifest,
	byName map[string]source.Source, o RestoreOptions) error {
	if len(m.Artifacts) != 1 {
		return errors.New("--local restores a single database; this snapshot has several sources")
	}
	a := m.Artifacts[0]
	pr, ok := byName[a.Source].(source.PortableRestorer)
	if !ok {
		return fmt.Errorf("source type %s cannot be restored locally", a.Kind)
	}
	major := o.PgVersion
	if major == 0 {
		f, _ := a.Meta["server_major"].(float64)
		major = int(f)
	}
	if major == 0 {
		major = 18
	}
	cli := e.Cfg.Verify.ContainerCLI
	db, _ := a.Meta["database"].(string)
	if db == "" {
		db = app
	}
	name := fmt.Sprintf("hbr-%s-%s", app, b.Time.Local().Format("20060102-1504"))

	fmt.Printf("\nPlan: start postgres:%d in %s as %q on 127.0.0.1 and restore into database %q\n", major, cli, name, db)
	if o.DryRun {
		if _, err := exec.LookPath(cli); err != nil {
			return fmt.Errorf("%s not found (set verify.container_cli to docker or podman)", cli)
		}
		if err := exec.CommandContext(ctx, cli, "info").Run(); err != nil {
			return fmt.Errorf("%s is not running (for podman: `podman machine start`)", cli)
		}
		fmt.Printf("✓ %s is running\n\nDry run: nothing started.\n", cli)
		return nil
	}

	port, err := freePort()
	if err != nil {
		return err
	}
	pw := randomHex(12)
	run := func(args ...string) (string, error) {
		var out, errb bytes.Buffer
		c := exec.CommandContext(ctx, cli, args...)
		c.Stdout, c.Stderr = &out, &errb
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("%s %s: %v: %s", cli, args[0], err, strings.TrimSpace(errb.String()))
		}
		return out.String(), nil
	}
	if _, err := run("run", "-d", "--name", name,
		"-e", "POSTGRES_PASSWORD="+pw, "-e", "POSTGRES_DB="+db,
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"--label", "hbr.app="+app,
		fmt.Sprintf("postgres:%d", major)); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			exec.Command(cli, "rm", "-f", name).Run()
		}
	}()
	e.Log.Printf("waiting for postgres:%d to start …", major)
	deadline := time.Now().Add(2 * time.Minute)
	for stable := 0; stable < 2; {
		if time.Now().After(deadline) {
			return errors.New("local postgres did not become ready")
		}
		if _, err := run("exec", name, "psql", "-U", "postgres", "-d", db, "-tAc", "select 1"); err == nil {
			stable++
		} else {
			stable = 0
		}
		time.Sleep(2 * time.Second)
	}

	url := fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/%s?sslmode=disable", pw, port, db)
	res, err := pr.RestoreTo(ctx, e.Env, dirFor(dir, a), a, source.Target{URL: url, Password: pw})
	if err != nil {
		return err
	}
	cleanup = false
	fmt.Printf(`
✓ restored %s into a local container — %s

  Connect:  psql "%s"
  Or:       %s exec -it %s psql -U postgres -d %s
  Remove:   %s rm -f %s

The container keeps running (it does not survive a reboot of the %s VM) until you remove it.
`, app, res, url, cli, name, db, cli, name, cli)
	return nil
}

func dirFor(dir string, a manifest.Artifact) string { return dir + "/" + a.Source }

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
