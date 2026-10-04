// Package secret stores database passwords in the macOS Keychain, so the
// config file never contains credentials.
package secret

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const service = "hbr"

// Set stores (or replaces) the password for account.
func Set(account, password string) error {
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", password)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// Get returns the password for account.
func Get(account string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("no password in Keychain for %q (re-run `hbr setup`)", account)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// Delete removes the password for account, if any.
func Delete(account string) error {
	err := exec.Command("security", "delete-generic-password", "-s", service, "-a", account).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil // not found
	}
	return err
}
