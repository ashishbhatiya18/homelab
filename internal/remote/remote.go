// Package remote runs commands on configured hosts over the system ssh client,
// so the user's existing keys and ~/.ssh/config are reused as-is.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

type Runner struct {
	Hosts          map[string]string // alias -> ssh target
	ConnectTimeout time.Duration
	ExtraOptions   []string
}

func (r *Runner) target(host string) (string, error) {
	t, ok := r.Hosts[host]
	if !ok || t == "" {
		return "", fmt.Errorf("unknown host %q", host)
	}
	return t, nil
}

func (r *Runner) args(target, cmd string) []string {
	a := []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(r.ConnectTimeout.Seconds())),
		"-o", "ServerAliveInterval=15",
	}
	for _, o := range r.ExtraOptions {
		a = append(a, "-o", o)
	}
	return append(a, target, cmd)
}

// Run executes cmd on host. stdin/stdout may be nil. On failure the remote
// stderr is included in the error.
func (r *Runner) Run(ctx context.Context, host, cmd string, stdin io.Reader, stdout io.Writer) error {
	t, err := r.target(host)
	if err != nil {
		return err
	}
	c := exec.CommandContext(ctx, "ssh", r.args(t, cmd)...)
	var stderr bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %s: %w: %s", host, firstWords(cmd, 6), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Output runs cmd and returns its stdout.
func (r *Runner) Output(ctx context.Context, host, cmd string) (string, error) {
	var out bytes.Buffer
	err := r.Run(ctx, host, cmd, nil, &out)
	return out.String(), err
}

// Reachable reports whether host accepts an ssh session right now.
func (r *Runner) Reachable(ctx context.Context, host string) bool {
	ctx, cancel := context.WithTimeout(ctx, r.ConnectTimeout+5*time.Second)
	defer cancel()
	return r.Run(ctx, host, "true", nil, nil) == nil
}

// Quote single-quotes s for a POSIX shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = append(f[:n], "…")
	}
	return strings.Join(f, " ")
}
