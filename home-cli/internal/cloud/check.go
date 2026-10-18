package cloud

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Probe is the outcome of one end-to-end request through Cloudflare's edge.
type Probe struct {
	Host     string
	Status   int
	Location string // redirect target host, if any
	CFError  string // Cloudflare error code from the body (e.g. 1033: tunnel down)
	Latency  time.Duration
	Err      error
}

// Problem explains a failed probe, or is "" when the route works.
func (p Probe) Problem() string {
	switch {
	case p.Err != nil:
		return p.Err.Error()
	case p.CFError == "1033":
		return "Cloudflare can't reach the tunnel (error 1033): is cloudflared running?"
	case p.CFError != "":
		return "Cloudflare error " + p.CFError
	case p.Status == 502 || p.Status == 504:
		return fmt.Sprintf("HTTP %d: cloudflared can't reach the origin service", p.Status)
	case p.Status >= 500:
		return fmt.Sprintf("HTTP %d from the origin", p.Status)
	}
	return ""
}

// Warning notes a route that answers but maybe not as intended: a 4xx at /
// is normal for APIs (the request did reach the origin), but it is also what
// a missing Traefik router looks like.
func (p Probe) Warning() string {
	if p.Err == nil && p.CFError == "" && p.Status >= 400 && p.Status < 500 && p.Status != 401 && p.Status != 403 {
		return fmt.Sprintf("HTTP %d at / (fine for an API; otherwise check the Traefik router)", p.Status)
	}
	return ""
}

// Result is the status in words, e.g. "302 → auth.ab18.in".
func (p Probe) Result() string {
	if p.Err != nil {
		return "-"
	}
	s := fmt.Sprint(p.Status)
	if p.Location != "" {
		s += " → " + p.Location
	}
	return s
}

// PublicResolver answers like the internet does. Local DNS (e.g. Pi-hole)
// often points these hostnames straight at the LAN, which would bypass the
// tunnel being checked.
var PublicResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, "1.1.1.1:53")
	},
}

var cfErrRE = regexp.MustCompile(`Error (\d{4})\b`)

// ProbeHost requests https://host/ through Cloudflare, without following
// redirects.
func ProbeHost(ctx context.Context, host string) Probe {
	p := Probe{Host: host}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	addrs, err := PublicResolver.LookupHost(ctx, host)
	if err != nil {
		p.Err = fmt.Errorf("public DNS: %w", err)
		return p
	}
	// IPv4 first: an AAAA answer is useless on a network without IPv6 routing.
	sort.SliceStable(addrs, func(i, j int) bool {
		return strings.Contains(addrs[j], ":") && !strings.Contains(addrs[i], ":")
	})
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var err error
			for _, a := range addrs {
				var c net.Conn
				if c, err = dialer.DialContext(ctx, network, net.JoinHostPort(a, "443")); err == nil {
					return c, nil
				}
			}
			return nil, err
		},
		TLSClientConfig:   &tls.Config{ServerName: host},
		ForceAttemptHTTP2: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/", nil)
	req.Header.Set("User-Agent", "home-route-check")
	start := time.Now()
	resp, err := client.Do(req)
	p.Latency = time.Since(start)
	if err != nil {
		p.Err = err
		return p
	}
	defer resp.Body.Close()
	p.Status = resp.StatusCode
	if loc := resp.Header.Get("Location"); loc != "" {
		if u, err := url.Parse(loc); err == nil {
			if u.Host == "" || u.Host == host {
				p.Location = u.Path
			} else {
				p.Location = u.Host
			}
		}
	}
	if resp.StatusCode >= 500 && resp.Header.Get("Server") == "cloudflare" {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		p.CFError = cfErrorCode(string(body))
	}
	return p
}

// cfErrorCode finds "Error 1033" in a Cloudflare error page.
func cfErrorCode(body string) string {
	if m := cfErrRE.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}
