// Package remote runs commands on configured hosts over the system ssh client,
// so the user's existing keys and ~/.ssh/config are reused as-is.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Runner struct {
	Hosts map[string]string // alias -> ssh target
	// Fallback maps an alias to a second ssh target (e.g. its Tailscale IP),
	// used when the first doesn't answer — off the LAN.
	Fallback       map[string]string
	ConnectTimeout time.Duration
	ExtraOptions   []string

	mu     sync.Mutex
	chosen map[string]string // alias -> target picked for this process
}

// probeTimeout bounds the TCP check that decides between a host's targets.
var probeTimeout = 1500 * time.Millisecond

func (r *Runner) target(host string) (string, error) {
	t, ok := r.Hosts[host]
	if !ok || t == "" {
		return "", fmt.Errorf("unknown host %q", host)
	}
	alt := r.Fallback[host]
	if alt == "" || alt == t {
		return t, nil
	}
	r.mu.Lock()
	c, ok := r.chosen[host]
	r.mu.Unlock()
	if ok && (c == t || c == alt) {
		return c, nil
	}
	c = t
	if !r.answers(t) && r.answers(alt) {
		c = alt
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chosen == nil {
		r.chosen = map[string]string{}
	}
	r.chosen[host] = c
	return c, nil
}

// Target returns the ssh target host is reached at (the fallback when the
// primary doesn't answer).
func (r *Runner) Target(host string) string {
	t, _ := r.target(host)
	return t
}

// answers reports whether target's ssh port accepts a TCP connection,
// resolving aliases, users and ports the way ssh does.
func (r *Runner) answers(target string) bool {
	addr := r.sshAddr(target)
	if addr == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", addr, probeTimeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func (r *Runner) sshAddr(target string) string {
	args := []string{"-G"}
	for _, o := range r.ExtraOptions {
		args = append(args, "-o", o)
	}
	out, err := exec.Command("ssh", append(args, target)...).Output()
	if err != nil {
		return ""
	}
	return parseSSHG(string(out))
}

// parseSSHG extracts host:port from `ssh -G` output.
func parseSSHG(out string) string {
	host, port := "", "22"
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), " ")
		switch k {
		case "hostname":
			host = v
		case "port":
			port = v
		}
	}
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, port)
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

// Interactive runs cmd on host with a terminal attached (ssh -t), wiring the
// local stdin/stdout/stderr through, for shells and exec sessions.
func (r *Runner) Interactive(ctx context.Context, host, cmd string) error {
	t, err := r.target(host)
	if err != nil {
		return err
	}
	args := append([]string{"-t"}, r.args(t, cmd)...)
	c := exec.CommandContext(ctx, "ssh", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
