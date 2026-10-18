package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ashishbhatiya18/home/internal/remote"
)

func inode(t *testing.T, p string) uint64 {
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

func runTokenScript(t *testing.T, path, tok string) {
	c := exec.Command("bash", "-c", fmt.Sprintf(tokenScript, remote.Quote(path)))
	c.Stdin = strings.NewReader(tok)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestTokenScript(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "ab-cloudflared.env")
	os.WriteFile(env, []byte("# tunnel\nTUNNEL_TOKEN=old\nOTHER=1\n"), 0o600)
	before := inode(t, env)
	runTokenScript(t, env, "eyJ/new+tok=")
	b, _ := os.ReadFile(env)
	if string(b) != "# tunnel\nTUNNEL_TOKEN=eyJ/new+tok=\nOTHER=1\n" || inode(t, env) != before {
		t.Fatalf("env file = %q (inode kept: %v)", b, inode(t, env) == before)
	}

	raw := filepath.Join(dir, "tok")
	os.WriteFile(raw, []byte("old"), 0o600)
	before = inode(t, raw)
	runTokenScript(t, raw, "new")
	if b, _ := os.ReadFile(raw); string(b) != "new" || inode(t, raw) != before {
		t.Fatalf("raw file = %q", b)
	}

	fresh := filepath.Join(dir, "fresh")
	runTokenScript(t, fresh, "abc")
	fi, _ := os.Stat(fresh)
	if b, _ := os.ReadFile(fresh); string(b) != "abc" || fi.Mode().Perm() != 0o600 {
		t.Fatalf("fresh file = %q %v", b, fi.Mode())
	}
}
