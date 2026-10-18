// Package ui draws a single updating status line on stderr and formats sizes
// and durations.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// Line is the live status line. On a non-terminal, updates are dropped and
// only Done/Info lines are printed.
type Line struct {
	tty  bool
	last string
}

func NewLine() *Line {
	return &Line{tty: term.IsTerminal(int(os.Stderr.Fd()))}
}

func width() int {
	w, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil || w < 20 {
		return 80
	}
	return w
}

// Update replaces the status line.
func (l *Line) Update(s string) {
	if !l.tty || s == l.last {
		return
	}
	l.last = s
	fmt.Fprint(os.Stderr, "\r\x1b[K"+Truncate(s, width()-1))
}

// Info prints a permanent line above the status line.
func (l *Line) Info(format string, a ...any) {
	l.clear()
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}

func (l *Line) clear() {
	if l.tty && l.last != "" {
		fmt.Fprint(os.Stderr, "\r\x1b[K")
		l.last = ""
	}
}

// Clear removes the status line.
func (l *Line) Clear() { l.clear() }

// Truncate shortens s to n runes, ending in an ellipsis.
func Truncate(s string, n int) string {
	if n <= 1 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// Bytes formats a size in binary units: 512 B, 3.4 MiB.
func Bytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f, units := float64(n), []string{"KiB", "MiB", "GiB", "TiB"}
	i := -1
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if f >= 100 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// Duration formats d compactly: 45s, 3m10s, 1h05m.
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// Progress renders "45% 120 MiB/266 MiB · 5.1 MiB/s · ETA 29s"; total 0
// means unknown.
func Progress(done, total, speed int64) string {
	var b strings.Builder
	if total > 0 {
		fmt.Fprintf(&b, "%3.0f%% %s/%s", float64(done)*100/float64(total), Bytes(done), Bytes(total))
	} else {
		b.WriteString(Bytes(done))
	}
	if speed > 0 {
		fmt.Fprintf(&b, " · %s/s", Bytes(speed))
		if total > done {
			fmt.Fprintf(&b, " · ETA %s", Duration(time.Duration(float64(total-done)/float64(speed)*float64(time.Second))))
		}
	}
	return b.String()
}

// Ask reads one line from the terminal (so it works while stdin is piped).
func Ask(prompt string) (string, error) {
	t, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", errors.New("this needs an interactive terminal")
	}
	defer t.Close()
	fmt.Fprint(t, prompt)
	s, err := bufio.NewReader(t).ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// Confirm asks a yes/no question; the default is no.
func Confirm(prompt string) bool {
	a, err := Ask(prompt + " [y/N] ")
	return err == nil && (strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"))
}
