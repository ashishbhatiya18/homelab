package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/remote"
	"golang.org/x/term"
)

const editUsage = "usage: home node edit <node> <path> [--sudo]   (relative paths start next to stacks_dir, e.g. secrets/app.env)"

// nodePath resolves a path given to `home node edit`: absolute paths are
// taken as they are, relative ones start at the directory holding stacks_dir
// (where a node keeps stacks/, data/ and secrets/).
func nodePath(n *homecfg.Node, p string) (string, error) {
	if p == "" || strings.HasSuffix(p, "/") {
		return "", errors.New("name a file, not a directory")
	}
	if strings.HasPrefix(p, "~") {
		return "", errors.New("~ is not expanded on the node; use an absolute path or one relative to " + path.Dir(n.StacksDir))
	}
	if !path.IsAbs(p) {
		p = path.Join(path.Dir(n.StacksDir), p)
	}
	return path.Clean(p), nil
}

// editCmd opens a file from a node in the local editor and writes it back
// when it changed. The file is written in place (same inode, owner and mode),
// so single-file bind mounts see the new content.
func (a *app) editCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("node edit", flag.ExitOnError)
	sudo := fs.Bool("sudo", false, "read and write the file as root")
	fs.Parse(reorder(args))
	if fs.NArg() != 2 {
		return errors.New(editUsage)
	}
	n, err := a.cfg.Node(fs.Arg(0))
	if err != nil {
		return err
	}
	file, err := nodePath(n, fs.Arg(1))
	if err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("home node edit needs an interactive terminal")
	}
	as, f := "", remote.Quote(file)
	if *sudo {
		as = "sudo -n "
	}
	hint := func(err error) error {
		if !*sudo && strings.Contains(err.Error(), "Permission denied") {
			return fmt.Errorf("%w (try --sudo)", err)
		}
		return err
	}

	kind, err := a.r.Output(ctx, n.Name, fmt.Sprintf("if %[1]stest -d %[2]s; then echo dir; elif %[1]stest -e %[2]s; then echo file; else echo new; fi", as, f))
	if err != nil {
		return err
	}
	kind = strings.TrimSpace(kind)
	var before string
	switch kind {
	case "dir":
		return fmt.Errorf("%s:%s is a directory", n.Name, file)
	case "file":
		if before, err = a.r.Output(ctx, n.Name, as+"cat "+f); err != nil {
			return hint(err)
		}
	default:
		logf("%s: %s does not exist yet; saving creates it (mode 600)", n.Name, file)
	}
	if strings.HasPrefix(file, n.StacksDir+"/") {
		logf("%s is a deployed file: the next `home deploy` puts the bundle's version back — change it in git to keep it", file)
	}

	// The copy may hold secrets: a private directory, removed afterwards.
	dir, err := os.MkdirTemp("", "home-edit-")
	if err != nil {
		return err
	}
	local := filepath.Join(dir, path.Base(file))
	if err := os.WriteFile(local, []byte(before), 0o600); err != nil {
		os.RemoveAll(dir)
		return err
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	// Not tied to ctx: the editor owns the terminal and handles Ctrl-C itself.
	ed := exec.Command("sh", "-c", editor+` "$1"`, "sh", local)
	ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := ed.Run(); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("%s: %w — nothing changed", editor, err)
	}
	after, err := os.ReadFile(local)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	if string(after) == before {
		os.RemoveAll(dir)
		fmt.Println("No changes.")
		return nil
	}

	// Refuse to overwrite what someone else changed in the meantime.
	sum := sha256.Sum256([]byte(before))
	guard := fmt.Sprintf(`[ "$(%scat "$f" | sha256sum | cut -d' ' -f1)" = %s ] || { echo "the file changed on the node while you were editing" >&2; exit 1; }`, as, hex.EncodeToString(sum[:]))
	if kind == "new" {
		guard = fmt.Sprintf(`%[1]stest ! -e "$f" || { echo "the file was created on the node while you were editing" >&2; exit 1; }
%[1]sinstall -m 600 /dev/null "$f"`, as)
	}
	// Staged in a temp file first so a dropped connection cannot truncate it.
	script := fmt.Sprintf(`set -e
f=%s
t=$(mktemp); trap 'rm -f "$t"' EXIT
cat > "$t"
%s
%stee "$f" < "$t" > /dev/null`, f, guard, as)
	if err := a.r.Run(ctx, n.Name, script, strings.NewReader(string(after)), nil); err != nil {
		return fmt.Errorf("%w\nyour edit is kept in %s", hint(err), local)
	}
	os.RemoveAll(dir)
	logf("%s: saved %s — restart or recreate what reads it for the change to apply", n.Name, file)
	return nil
}
