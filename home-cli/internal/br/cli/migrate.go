package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ashishbhatiya18/home/internal/br/config"
)

// migrateFromHbr copies config and state from the standalone hbr tool
// (~/.config/hbr, ~/.local/state/hbr) to home's locations the first time.
// Originals are left in place; snapshots and keys never move.
func migrateFromHbr() error {
	h, _ := os.UserHomeDir()
	newCfg := config.DefaultPath()
	if os.Getenv("HOME_BR_CONFIG") != "" {
		return nil
	}
	oldCfg := filepath.Join(h, ".config", "hbr", "config.yaml")
	if _, err := os.Stat(newCfg); err == nil {
		return nil
	}
	if _, err := os.Stat(oldCfg); err != nil {
		return nil
	}
	if err := copyFile(oldCfg, newCfg); err != nil {
		return fmt.Errorf("migrating hbr config: %w", err)
	}
	oldState := filepath.Join(h, ".local", "state", "hbr", "state.json")
	if _, err := os.Stat(oldState); err == nil {
		if err := copyFile(oldState, filepath.Join(h, ".local", "state", "home", "br", "state.json")); err != nil {
			return fmt.Errorf("migrating hbr state: %w", err)
		}
	}
	fmt.Fprintf(os.Stderr, "Migrated hbr settings to %s (originals kept in ~/.config/hbr and ~/.local/state/hbr).\n", newCfg)
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
