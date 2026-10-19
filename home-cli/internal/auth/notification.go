package auth

import (
	"strings"
	"time"
)

// Notification is the message Authelia's filesystem notifier last wrote; the
// file only ever holds the latest one.
type Notification struct {
	Date      time.Time
	Recipient string
	Subject   string
	// Code is the one-time code of an identity verification, "" for others.
	Code string
	Body string
}

// ParseNotification reads notification.txt: Date/Recipient/Subject headers,
// then the plain-text body. A one-time code message starts with "A ONE-TIME
// CODE HAS BEEN GENERATED" and has the code as the only line between the
// first two dashed separator lines.
func ParseNotification(s string) (Notification, bool) {
	var n Notification
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	i := 0
headers:
	for ; i < len(lines); i++ {
		k, v, _ := strings.Cut(lines[i], ": ")
		switch k {
		case "Date":
			// "2026-10-19 01:27:43.407110378 +0000 UTC m=+2372.097546972"
			f := strings.Fields(v)
			if len(f) >= 3 {
				n.Date, _ = time.Parse("2006-01-02 15:04:05.999999999 -0700", f[0]+" "+f[1]+" "+f[2])
			}
		case "Recipient":
			n.Recipient = strings.Trim(v, "{}")
		case "Subject":
			n.Subject = v
		default:
			break headers
		}
	}
	if n.Subject == "" {
		return Notification{}, false
	}
	n.Body = strings.TrimSpace(strings.Join(lines[i:], "\n"))
	var sep []int
	for j, l := range lines[i:] {
		if t := strings.TrimSpace(l); len(t) >= 10 && strings.Trim(t, "-") == "" {
			sep = append(sep, i+j)
		}
		if len(sep) == 2 {
			break
		}
	}
	if len(sep) == 2 && strings.HasPrefix(n.Body, "A ONE-TIME CODE HAS BEEN GENERATED") {
		var inner []string
		for _, l := range lines[sep[0]+1 : sep[1]] {
			if t := strings.TrimSpace(l); t != "" {
				inner = append(inner, t)
			}
		}
		if len(inner) == 1 && !strings.Contains(inner[0], " ") {
			n.Code = inner[0]
		}
	}
	return n, true
}
