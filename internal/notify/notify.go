// Package notify shows desktop notifications (macOS) and is a no-op elsewhere.
package notify

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
)

// Send shows a notification; failures are ignored (notifications are best-effort).
func Send(title, message string) {
	if os.Getenv("HBR_NO_NOTIFY") != "" || runtime.GOOS != "darwin" {
		return
	}
	script := "display notification " + strconv.Quote(message) + " with title " + strconv.Quote(title)
	_ = exec.Command("osascript", "-e", script).Run()
}
