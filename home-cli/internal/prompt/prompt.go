// Package prompt reads passwords and confirmations from the controlling
// terminal, so they never end up in shell history or process arguments.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// PassphraseEnv lets automated tests supply the password. Never set it for
// the service: daily backups do not need a password at all.
const PassphraseEnv = "HOME_BR_PASSPHRASE"

func tty() (*os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("this step needs an interactive terminal")
	}
	return f, nil
}

// Password asks for a password without echoing it.
func Password(label string) (string, error) {
	if v := os.Getenv(PassphraseEnv); v != "" {
		return v, nil
	}
	t, err := tty()
	if err != nil {
		return "", err
	}
	defer t.Close()
	fmt.Fprint(t, label)
	b, err := term.ReadPassword(int(t.Fd()))
	fmt.Fprintln(t)
	return string(b), err
}

// NewPassword asks twice and enforces a minimum length.
func NewPassword(label string) (string, error) {
	p1, err := Password(label)
	if err != nil {
		return "", err
	}
	if len(p1) < 12 {
		return "", errors.New("use at least 12 characters")
	}
	if os.Getenv(PassphraseEnv) != "" {
		return p1, nil
	}
	p2, err := Password("Repeat password: ")
	if err != nil {
		return "", err
	}
	if p1 != p2 {
		return "", errors.New("passwords do not match")
	}
	return p1, nil
}

// Line reads one line of visible input.
func Line(label string) (string, error) {
	t, err := tty()
	if err != nil {
		return "", err
	}
	defer t.Close()
	fmt.Fprint(t, label)
	s, err := bufio.NewReader(t).ReadString('\n')
	return strings.TrimSpace(s), err
}

// Confirm requires the user to type want exactly.
func Confirm(label, want string) error {
	got, err := Line(label)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("cancelled (expected %q)", want)
	}
	return nil
}

// Ask shows a question with a default; empty input returns the default.
func Ask(label, def string) (string, error) {
	q := label
	if def != "" {
		q += " [" + def + "]"
	}
	s, err := Line(q + ": ")
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

// YesNo asks a yes/no question.
func YesNo(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		s, err := Line(fmt.Sprintf("%s [%s]: ", label, hint))
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// Choose shows a numbered list and returns the chosen index.
func Choose(label string, options []string, def int) (int, error) {
	t, err := tty()
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(t, label)
	for i, o := range options {
		fmt.Fprintf(t, "  %d) %s\n", i+1, o)
	}
	t.Close()
	for {
		s, err := Ask("Choose", fmt.Sprint(def+1))
		if err != nil {
			return 0, err
		}
		var n int
		if _, err := fmt.Sscan(s, &n); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
	}
}

// ChooseMany accepts "1,3", "2-4" or "all"; returns chosen indexes.
func ChooseMany(label string, options []string) ([]int, error) {
	t, err := tty()
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(t, label)
	for i, o := range options {
		fmt.Fprintf(t, "  %d) %s\n", i+1, o)
	}
	t.Close()
	for {
		s, err := Ask("Choose (e.g. 1,3 or all)", "all")
		if err != nil {
			return nil, err
		}
		if idx, ok := parseSelection(s, len(options)); ok {
			return idx, nil
		}
	}
}

func parseSelection(s string, n int) ([]int, bool) {
	if strings.EqualFold(strings.TrimSpace(s), "all") {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out, true
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		var a, b int
		if c, _ := fmt.Sscanf(part, "%d-%d", &a, &b); c == 2 {
		} else if c, _ := fmt.Sscanf(part, "%d", &a); c == 1 {
			b = a
		} else {
			return nil, false
		}
		if a < 1 || b > n || a > b {
			return nil, false
		}
		for i := a; i <= b; i++ {
			if !seen[i-1] {
				seen[i-1] = true
				out = append(out, i-1)
			}
		}
	}
	return out, len(out) > 0
}
