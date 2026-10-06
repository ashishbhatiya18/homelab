// Package keys manages the age keys that encrypt backups.
//
// Every backup is encrypted to two X25519 recipients:
//
//   - the daily key, whose private half is stored encrypted with the user's
//     password (daily-key.age), so backups need no password but reading them does;
//   - the recovery key, whose private half is shown once at init and kept
//     offline (e.g. in a password manager). Only its public half is stored.
//
// Either private key decrypts any backup.
package keys

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
)

const (
	DailyKeyFile   = "daily-key.age"
	RecipientsFile = "recipients.txt"
)

func Exists(dir string) bool {
	_, err1 := os.Stat(filepath.Join(dir, DailyKeyFile))
	_, err2 := os.Stat(filepath.Join(dir, RecipientsFile))
	return err1 == nil && err2 == nil
}

// Init creates both keys and returns the recovery private key, which the
// caller must show to the user exactly once.
func Init(dir, passphrase string) (string, error) {
	if Exists(dir) {
		return "", fmt.Errorf("keys already exist in %s", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	daily, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	recovery, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := writeDaily(dir, daily, passphrase); err != nil {
		return "", err
	}
	rec := fmt.Sprintf("# hbr recipients. Public keys only; safe to share.\n"+
		"# daily key (private half: %s, password-protected)\n%s\n"+
		"# recovery key (private half kept offline)\n%s\n",
		DailyKeyFile, daily.Recipient(), recovery.Recipient())
	if err := os.WriteFile(filepath.Join(dir, RecipientsFile), []byte(rec), 0o644); err != nil {
		return "", err
	}
	return recovery.String(), nil
}

func writeDaily(dir string, id *age.X25519Identity, passphrase string) error {
	sr, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, sr)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, id.String()+"\n"); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := aw.Close(); err != nil {
		return err
	}
	tmp := filepath.Join(dir, DailyKeyFile+".tmp")
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, DailyKeyFile))
}

func Recipients(dir string) ([]age.Recipient, error) {
	f, err := os.Open(filepath.Join(dir, RecipientsFile))
	if err != nil {
		return nil, fmt.Errorf("no keys in %s (run `hbr init`): %w", dir, err)
	}
	defer f.Close()
	r, err := age.ParseRecipients(f)
	if err != nil {
		return nil, err
	}
	if len(r) < 2 {
		return nil, errors.New("recipients.txt must list the daily and recovery keys")
	}
	return r, nil
}

// DailyIdentity unlocks the daily private key with the password.
func DailyIdentity(dir, passphrase string) (*age.X25519Identity, error) {
	f, err := os.Open(filepath.Join(dir, DailyKeyFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	si, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(armor.NewReader(f), si)
	if err != nil {
		return nil, errors.New("wrong password (or damaged key file)")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return age.ParseX25519Identity(strings.TrimSpace(string(b)))
}

// ParseRecovery parses a recovery key as pasted by the user.
func ParseRecovery(s string) (*age.X25519Identity, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(s))
	if err != nil {
		return nil, errors.New("that is not a valid recovery key (it starts with AGE-SECRET-KEY-1)")
	}
	return id, nil
}

// MatchesRecipients reports whether id is one of the configured recipients.
func MatchesRecipients(dir string, id *age.X25519Identity) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, RecipientsFile))
	if err != nil {
		return false, err
	}
	return strings.Contains(string(b), id.Recipient().String()), nil
}

// ChangePassword re-encrypts the daily key; backups are unaffected.
func ChangePassword(dir, oldPass, newPass string) error {
	id, err := DailyIdentity(dir, oldPass)
	if err != nil {
		return err
	}
	return writeDaily(dir, id, newPass)
}
