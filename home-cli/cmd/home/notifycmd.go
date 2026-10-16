package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ashishbhatiya18/home/internal/br/secret"
	"github.com/ashishbhatiya18/home/internal/notify"
	"github.com/ashishbhatiya18/home/internal/prompt"
)

const notifyUsage = "usage: home notify setup|test|off"

// notifyCmd manages the ntfy push settings, kept in the Keychain because the
// topic URLs and token are credentials.
func notifyCmd(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New(notifyUsage)
	}
	switch args[0] {
	case "setup":
		return notifySetup(ctx)
	case "test":
		return notifyTest(ctx)
	case "off":
		for _, a := range []string{notify.AccountURL, notify.AccountToken, notify.AccountFallback} {
			if err := secret.Delete(a); err != nil {
				return err
			}
		}
		fmt.Println("✓ ntfy settings removed; notifications are desktop-only again.")
		return nil
	}
	return errors.New(notifyUsage)
}

func notifySetup(ctx context.Context) error {
	fmt.Println("Notifications are also pushed to an ntfy topic; if it can't be reached, to a")
	fmt.Println("fallback topic (e.g. on ntfy.sh). Everything is saved in the macOS Keychain.")
	current := func(a string) string { v, _ := secret.Get(a); return v }

	primary, err := prompt.Ask("ntfy topic URL (e.g. https://ntfy.example.com/homelab)", current(notify.AccountURL))
	if err != nil {
		return err
	}
	token, err := prompt.Password("Access token (tk_…; empty keeps the saved one, '-' for none): ")
	if err != nil {
		return err
	}
	fallback, err := prompt.Ask("Fallback topic URL, sent without the token ('-' for none)", current(notify.AccountFallback))
	if err != nil {
		return err
	}

	set := func(account, v string) error {
		v = strings.TrimSpace(v)
		if v == "-" || v == "" {
			return secret.Delete(account)
		}
		return secret.Set(account, v)
	}
	if err := set(notify.AccountURL, primary); err != nil {
		return err
	}
	if token != "" {
		if err := set(notify.AccountToken, token); err != nil {
			return err
		}
	}
	if err := set(notify.AccountFallback, fallback); err != nil {
		return err
	}
	return notifyTest(ctx)
}

func notifyTest(ctx context.Context) error {
	used, err := notify.Push(ctx, "home: test ✓", "Notifications from home reach this device.")
	if err != nil {
		return err
	}
	fmt.Println("✓ sent to", used)
	return nil
}
