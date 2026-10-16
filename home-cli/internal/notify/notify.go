// Package notify shows desktop notifications (macOS) and, once `home notify
// setup` has saved a topic, pushes them to ntfy too. When that topic can't be
// reached (e.g. the node hosting ntfy is down) the push goes to a fallback
// topic, typically on ntfy.sh. Notifications are best-effort: failures are
// ignored.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"time"

	"github.com/ashishbhatiya18/home/internal/br/secret"
)

// Keychain accounts holding the ntfy settings, all single-line values.
const (
	AccountURL      = "ntfy:url"      // topic URL, e.g. https://ntfy.ab18.in/homelab
	AccountToken    = "ntfy:token"    // access token (tk_…) for that topic
	AccountFallback = "ntfy:fallback" // topic URL used when the first fails, sent without the token
)

// ErrNotConfigured is returned by Push when no ntfy topic is saved.
var ErrNotConfigured = errors.New("ntfy is not set up — run `home notify setup`")

// get reads a setting; tests replace it.
var get = func(account string) string {
	v, _ := secret.Get(account)
	return v
}

var client = &http.Client{Timeout: 10 * time.Second}

// Send shows a notification and pushes it to ntfy.
func Send(title, message string) {
	if os.Getenv("HOME_NO_NOTIFY") != "" {
		return
	}
	if runtime.GOOS == "darwin" {
		script := "display notification " + strconv.Quote(message) + " with title " + strconv.Quote(title)
		_ = exec.Command("osascript", "-e", script).Run()
	}
	_, _ = Push(context.Background(), title, message)
}

// Push sends a message to the saved ntfy topic, or to the fallback topic when
// that fails. It returns the topic URL that accepted it.
func Push(ctx context.Context, title, message string) (string, error) {
	primary, fallback := get(AccountURL), get(AccountFallback)
	if primary == "" && fallback == "" {
		return "", ErrNotConfigured
	}
	var errs []error
	if primary != "" {
		err := publish(ctx, primary, get(AccountToken), title, message)
		if err == nil {
			return primary, nil
		}
		errs = append(errs, err)
	}
	if fallback != "" {
		err := publish(ctx, fallback, "", title, message)
		if err == nil {
			return fallback, nil
		}
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}

// publish posts JSON to the server root (ntfy's JSON publishing), which keeps
// non-ASCII titles intact, unlike the Title header.
func publish(ctx context.Context, topicURL, token, title, message string) error {
	u, err := url.Parse(topicURL)
	if err != nil || u.Host == "" || path.Base(u.Path) == "/" || path.Base(u.Path) == "." {
		return fmt.Errorf("bad ntfy topic URL %q (want https://host/topic)", topicURL)
	}
	topic := path.Base(u.Path)
	u.Path = path.Dir(u.Path)
	body, _ := json.Marshal(map[string]string{"topic": topic, "title": title, "message": message})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", topicURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", topicURL, resp.Status)
	}
	return nil
}
