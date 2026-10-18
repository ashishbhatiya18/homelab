package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ashishbhatiya18/home/internal/cloud"
)

// Route and DNS edits change the files in the cloud directory and then run
// the same plan/apply as `home cloud apply` (unless --no-apply). The edited
// files stay changed when the apply is cancelled; commit them with the rest.

type ruleFlags struct {
	fs                            *flag.FlagSet
	tunnel, service, path         *string
	hostHeader, serverName        *string
	noTLSVerify, http2, yes, only *bool
	set                           map[string]bool
}

func newRuleFlags(name string) *ruleFlags {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return &ruleFlags{
		fs:          fs,
		tunnel:      fs.String("tunnel", "", "tunnel to route through (default: the one routing the zone already)"),
		service:     fs.String("service", "", "origin service, e.g. https://traefik:443 or http://app:8080"),
		path:        fs.String("path", "", "only requests whose path matches this regex"),
		hostHeader:  fs.String("host-header", "", "Host header sent to the origin"),
		serverName:  fs.String("server-name", "", "TLS server name expected from the origin"),
		noTLSVerify: fs.Bool("no-tls-verify", false, "don't verify the origin's certificate"),
		http2:       fs.Bool("http2", false, "talk HTTP/2 to the origin"),
		yes:         fs.Bool("yes", false, "apply without asking"),
		only:        fs.Bool("no-apply", false, "only edit the files"),
	}
}

func (f *ruleFlags) parse(args []string) {
	f.fs.Parse(reorderFlags(args, "tunnel", "service", "path", "host-header", "server-name", "to", "ttl", "priority", "comment"))
	f.set = map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })
}

// overridden reports whether any origin setting was given.
func (f *ruleFlags) overridden() bool {
	for _, k := range []string{"service", "path", "host-header", "server-name", "no-tls-verify", "http2"} {
		if f.set[k] {
			return true
		}
	}
	return false
}

// apply puts the flags onto a full rule.
func (f *ruleFlags) apply(r cloud.Rule) cloud.Rule {
	or := map[string]any{}
	for k, v := range r.OriginRequest {
		or[k] = v
	}
	if f.set["service"] {
		r.Service = *f.service
	}
	if f.set["path"] {
		r.Path = *f.path
	}
	set := func(flagName, key string, v any) {
		if f.set[flagName] {
			if v == "" || v == false {
				delete(or, key)
			} else {
				or[key] = v
			}
		}
	}
	set("host-header", "httpHostHeader", *f.hostHeader)
	set("server-name", "originServerName", *f.serverName)
	set("no-tls-verify", "noTLSVerify", *f.noTLSVerify)
	set("http2", "http2Origin", *f.http2)
	r.OriginRequest = or
	if len(or) == 0 {
		r.OriginRequest = nil
	}
	return r
}

// reorderFlags is reorder for flags that take a value.
func reorderFlags(args []string, valued ...string) []string {
	takes := map[string]bool{}
	for _, v := range valued {
		takes["-"+v], takes["--"+v] = true, true
	}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if takes[a] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func (a *app) cloudFiles() (*cloud.Files, string, error) {
	if a.cfg.Cloudflare == nil || a.cfg.Cloudflare.Dir == "" {
		return nil, "", errors.New("Cloudflare is not set up — run `home cloud setup`")
	}
	dir := a.cfg.Cloudflare.CloudDir()
	fs, err := cloud.Load(dir)
	return fs, dir, err
}

// tunnelForHost picks the tunnel file for a new route: --tunnel, else the only
// tunnel already routing hostnames of the same zone.
func tunnelForHost(fs *cloud.Files, host, want string) (*cloud.TunnelFile, error) {
	if want != "" {
		if f := fs.Tunnel(want); f != nil {
			return f, nil
		}
		return nil, fmt.Errorf("no file for tunnel %q (tunnels/%s.yaml)", want, want)
	}
	var cands []*cloud.TunnelFile
	for i := range fs.Tunnels {
		f := &fs.Tunnels[i]
		for _, h := range f.Hostnames() {
			if sameZone(h, host) {
				cands = append(cands, f)
				break
			}
		}
	}
	if len(cands) == 1 {
		return cands[0], nil
	}
	var names []string
	for _, f := range fs.Tunnels {
		if !f.Local {
			names = append(names, f.Tunnel)
		}
	}
	return nil, fmt.Errorf("which tunnel? pass --tunnel (%s)", strings.Join(names, ", "))
}

// sameZone guesses that two hostnames share a zone by their last two labels.
func sameZone(a, b string) bool {
	last2 := func(h string) string {
		p := strings.Split(h, ".")
		if len(p) < 2 {
			return h
		}
		return strings.Join(p[len(p)-2:], ".")
	}
	return last2(a) == last2(b)
}

func (a *app) routeEdit(ctx context.Context, action string, args []string) error {
	rf := newRuleFlags("route " + action)
	to := rf.fs.String("to", "", "tunnel to move the route to")
	rf.parse(args)
	if rf.fs.NArg() != 1 {
		return errors.New(routeUsage)
	}
	host := strings.ToLower(rf.fs.Arg(0))
	fs, dir, err := a.cloudFiles()
	if err != nil {
		return err
	}
	cur, curRule := fs.RuleFor(host)

	switch action {
	case "add":
		if cur != nil {
			return fmt.Errorf("%s is already routed by %s — use `home route set` or `home route move`", host, cur.Tunnel)
		}
		f, err := tunnelForHost(fs, host, *rf.tunnel)
		if err != nil {
			return err
		}
		r := cloud.Rule{Hostname: host, Short: true}
		if rf.overridden() || f.Defaults == nil {
			if f.Defaults == nil && !rf.set["service"] {
				return fmt.Errorf("tunnel %s has no defaults: pass --service", f.Tunnel)
			}
			r = f.Shorten(rf.apply(f.Resolved(r)))
		}
		if _, err := cloud.SetRule(dir, f.Tunnel, r); err != nil {
			return err
		}
		fmt.Printf("✓ %s: added %s\n", relPath(dir, cloud.TunnelPath(dir, f.Tunnel)), host)
		a.warnNoRouter(ctx, f, f.Resolved(r))
	case "set":
		if cur == nil {
			return fmt.Errorf("%s is not routed — use `home route add`", host)
		}
		if !rf.overridden() {
			return errors.New("nothing to change: pass --service, --host-header, --server-name, --no-tls-verify, --http2 or --path")
		}
		r := cur.Shorten(rf.apply(cur.Resolved(*curRule)))
		if _, err := cloud.SetRule(dir, cur.Tunnel, r); err != nil {
			return err
		}
		fmt.Printf("✓ %s: changed %s\n", relPath(dir, cloud.TunnelPath(dir, cur.Tunnel)), host)
	case "remove":
		if cur == nil {
			return fmt.Errorf("%s is not routed", host)
		}
		if _, err := cloud.RemoveRule(dir, cur.Tunnel, host, "*"); err != nil {
			return err
		}
		fmt.Printf("✓ %s: removed %s (its DNS record goes with it)\n", relPath(dir, cloud.TunnelPath(dir, cur.Tunnel)), host)
	case "move":
		if cur == nil {
			return fmt.Errorf("%s is not routed — use `home route add`", host)
		}
		dst := fs.Tunnel(*to)
		if dst == nil {
			return fmt.Errorf("no file for tunnel %q: pass --to <tunnel>", *to)
		}
		if dst.Tunnel == cur.Tunnel {
			return fmt.Errorf("%s is already routed by %s", host, dst.Tunnel)
		}
		r := dst.Shorten(cur.Resolved(*curRule))
		if _, err := cloud.SetRule(dir, dst.Tunnel, r); err != nil {
			return err
		}
		if _, err := cloud.RemoveRule(dir, cur.Tunnel, host, curRule.Path); err != nil {
			return err
		}
		fmt.Printf("✓ moved %s from %s to %s\n", host, cur.Tunnel, dst.Tunnel)
		a.warnNoRouter(ctx, dst, r)
	default:
		return errors.New(routeUsage)
	}
	if *rf.only {
		fmt.Println("Files changed; `home cloud apply` applies them.")
		return nil
	}
	_, err = a.cloudPlan(ctx, true, *rf.yes)
	return err
}

// warnNoRouter warns when a route goes to Traefik on a node that has no
// router for the hostname (the route would answer 404).
func (a *app) warnNoRouter(ctx context.Context, f *cloud.TunnelFile, r cloud.Rule) {
	if f.Node == "" || !strings.Contains(f.Resolved(r).Service, "traefik") {
		return
	}
	if _, err := a.cfg.Node(f.Node); err != nil {
		return
	}
	routers, err := cloud.Routers(ctx, a.r, f.Node)
	if err != nil {
		return
	}
	if len((&cloud.State{Routers: routers}).RoutersFor(r.Hostname)) == 0 {
		fmt.Printf("⚠ no Traefik router on %s answers %s yet: add a Host(`%s`) router to its stack, or the route answers 404\n", f.Node, r.Hostname, r.Hostname)
	}
}

func relPath(dir, p string) string {
	if rel, ok := strings.CutPrefix(p, dir+string(os.PathSeparator)); ok {
		return rel
	}
	return p
}

// ---- home dns ----------------------------------------------------------------

const dnsUsage = "usage: home dns add <name> <type> <content> [--proxied] [--ttl N] [--priority N] [--comment …] | home dns remove <name> [type] [content]   (name is the full hostname; add --no-apply to only edit the files)"

func (a *app) dnsCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(dnsUsage)
	}
	fl := flag.NewFlagSet("dns "+args[0], flag.ExitOnError)
	proxied := fl.Bool("proxied", false, "proxied through Cloudflare")
	ttl := fl.Int("ttl", 0, "TTL in seconds (default automatic)")
	prio := fl.Int("priority", -1, "priority (MX, SRV, URI)")
	comment := fl.String("comment", "", "comment on the record")
	yes := fl.Bool("yes", false, "apply without asking")
	only := fl.Bool("no-apply", false, "only edit the files")
	fl.Parse(reorderFlags(args[1:], "ttl", "priority", "comment"))
	pos := fl.Args()

	fs, dir, err := a.cloudFiles()
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New(dnsUsage)
	}
	name := strings.ToLower(strings.TrimSuffix(pos[0], "."))
	zone := zoneOf(fs, name)
	if zone == "" {
		return fmt.Errorf("no zone file covers %s — `home cloud export` writes one per zone", name)
	}
	rel := cloud.RelName(name, zone)

	switch args[0] {
	case "add":
		if len(pos) != 3 {
			return errors.New(dnsUsage)
		}
		typ := strings.ToUpper(pos[1])
		if f, _ := fs.RuleFor(name); f != nil && typ == "CNAME" {
			return fmt.Errorf("%s is routed by tunnel %s, which already implies its CNAME", name, f.Tunnel)
		}
		r := cloud.Record{Name: rel, Type: typ, Content: pos[2], Proxied: *proxied, TTL: *ttl, Comment: *comment}
		if *prio >= 0 {
			p := *prio
			r.Priority = &p
		}
		if err := cloud.AddRecord(dir, zone, r); err != nil {
			return err
		}
		fmt.Printf("✓ %s: added %s %s %s\n", relPath(dir, cloud.ZonePath(dir, zone)), rel, typ, pos[2])
	case "remove":
		if len(pos) < 1 || len(pos) > 3 {
			return errors.New(dnsUsage)
		}
		removed, err := cloud.RemoveRecords(dir, zone, func(r cloud.Record) bool {
			return r.Name == rel && (len(pos) < 2 || strings.EqualFold(r.Type, pos[1])) && (len(pos) < 3 || r.Content == pos[2])
		})
		if err != nil {
			return err
		}
		if len(removed) == 0 {
			if f, _ := fs.RuleFor(name); f != nil {
				return fmt.Errorf("%s's record comes from its route on tunnel %s: `home route remove %s`", name, f.Tunnel, name)
			}
			return fmt.Errorf("no record %s in %s", strings.Join(pos, " "), relPath(dir, cloud.ZonePath(dir, zone)))
		}
		for _, r := range removed {
			fmt.Printf("✓ %s: removed %s %s %s\n", relPath(dir, cloud.ZonePath(dir, zone)), r.Name, r.Type, r.Content)
		}
	default:
		return errors.New(dnsUsage)
	}
	if *only {
		fmt.Println("Files changed; `home cloud apply` applies them.")
		return nil
	}
	_, err = a.cloudPlan(ctx, true, *yes)
	return err
}

// zoneOf finds the zone file a name belongs to (longest match).
func zoneOf(fs *cloud.Files, name string) string {
	best := ""
	for _, z := range fs.Zones {
		if (name == z.Zone || strings.HasSuffix(name, "."+z.Zone)) && len(z.Zone) > len(best) {
			best = z.Zone
		}
	}
	return best
}
