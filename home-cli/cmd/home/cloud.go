package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ashishbhatiya18/home/internal/br/secret"
	"github.com/ashishbhatiya18/home/internal/cloud"
	"github.com/ashishbhatiya18/home/internal/cloudflare"
	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/prompt"
)

// Keychain account of the Cloudflare API token.
const cloudflareTokenAccount = "cloudflare:token"

const (
	cloudUsage  = "usage: home cloud setup | plan | apply [--yes] | undo [--list] [--yes] | export [--dir <dir>]"
	tunnelUsage = "usage: home tunnel list | show <tunnel> | create <name> --node N --stack S [--container C] [--secret PATH] [--service URL] | token <tunnel> --install [--secret PATH] | token <tunnel> --print | rotate <tunnel> | delete <tunnel>"
	routeUsage  = "usage: home route list [tunnel|node|zone] [--all] | check [host…] | add <host> [--tunnel t] [origin flags] | set <host> <origin flags> | remove <host> | move <host> --to <tunnel>\n  origin flags: --service URL --host-header H --server-name N --no-tls-verify --http2 --path REGEX; --no-apply only edits the files, --yes applies without asking"
)

func (a *app) cloudAPI() (*cloudflare.Client, *homecfg.Cloudflare, error) {
	cf := a.cfg.Cloudflare
	if cf == nil || cf.AccountID == "" {
		return nil, nil, errors.New("Cloudflare is not set up — run `home cloud setup`")
	}
	tok, err := secret.Get(cloudflareTokenAccount)
	if err != nil || tok == "" {
		return nil, nil, errors.New("no Cloudflare API token in the Keychain — run `home cloud setup`")
	}
	return cloudflare.New(tok), cf, nil
}

func (a *app) gather(ctx context.Context, o cloud.GatherOpts) (*cloud.State, *homecfg.Cloudflare, error) {
	api, cf, err := a.cloudAPI()
	if err != nil {
		return nil, nil, err
	}
	st, err := cloud.Gather(ctx, api, cf.AccountID, a.cfg, a.r, o)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range st.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	return st, cf, nil
}

// ---- home cloud --------------------------------------------------------------

func (a *app) cloudCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(cloudUsage)
	}
	switch args[0] {
	case "setup":
		return a.cloudSetup(ctx)
	case "plan":
		_, err := a.cloudPlan(ctx, false, false)
		return err
	case "apply":
		fs := flag.NewFlagSet("cloud apply", flag.ExitOnError)
		yes := fs.Bool("yes", false, "do not ask for confirmation")
		fs.Parse(reorder(args[1:]))
		_, err := a.cloudPlan(ctx, true, *yes)
		return err
	case "undo":
		fs := flag.NewFlagSet("cloud undo", flag.ExitOnError)
		yes := fs.Bool("yes", false, "do not ask for confirmation")
		ls := fs.Bool("list", false, "list the saved snapshots")
		fs.Parse(reorder(args[1:]))
		return a.cloudUndo(ctx, *ls, *yes, fs.Arg(0))
	case "export":
		fs := flag.NewFlagSet("cloud export", flag.ExitOnError)
		dir := fs.String("dir", "", "write here instead of the configured directory")
		fs.Parse(reorder(args[1:]))
		return a.cloudExport(ctx, *dir)
	}
	return errors.New(cloudUsage)
}

func (a *app) cloudSetup(ctx context.Context) error {
	fmt.Println("home manages Cloudflare Tunnels, their routes and DNS with an API token kept in")
	fmt.Println("the macOS Keychain. Create a custom token (My Profile → API Tokens) with:")
	fmt.Println("  Account · Cloudflare Tunnel · Edit   (Read is enough for list/show/export)")
	fmt.Println("  Zone · DNS · Edit                    (Read is enough for list/show/export)")
	fmt.Println("  Zone · Zone · Read")
	fmt.Println("and include the zones whose DNS home should manage.")
	tok, err := prompt.Password("API token (empty keeps the saved one): ")
	if err != nil {
		return err
	}
	if tok = strings.TrimSpace(tok); tok == "" {
		if tok, _ = secret.Get(cloudflareTokenAccount); tok == "" {
			return errors.New("no token given and none saved")
		}
	}
	api := cloudflare.New(tok)
	accounts, err := api.VisibleAccounts(ctx)
	if err != nil {
		return fmt.Errorf("the token doesn't work: %w", err)
	}
	cf := homecfg.Cloudflare{}
	if a.cfg.Cloudflare != nil {
		cf = *a.cfg.Cloudflare
	}
	if len(accounts) == 0 {
		// Neither /accounts nor any zone names one: ask for the ID.
		id, err := prompt.Ask("Account ID (the hex string after dash.cloudflare.com/ in the dashboard URL)", cf.AccountID)
		if err != nil {
			return err
		}
		if id = strings.TrimSpace(id); id == "" {
			return errors.New("no account ID given")
		}
		accounts = []cloudflare.Account{{ID: id, Name: id}}
	}
	pick := 0
	if len(accounts) > 1 {
		var names []string
		for i, ac := range accounts {
			names = append(names, ac.Name+"  ("+ac.ID+")")
			if ac.ID == cf.AccountID {
				pick = i
			}
		}
		if pick, err = prompt.Choose("Account:", names, pick); err != nil {
			return err
		}
	}
	cf.AccountID = accounts[pick].ID
	fmt.Println("Account:", accounts[pick].Name)

	def := cf.Dir
	if def == "" {
		def = defaultCloudDir()
	}
	if cf.Dir, err = prompt.Ask("Directory for the tunnel and zone files (in your git repo)", def); err != nil {
		return err
	}

	tunnels, err := api.Tunnels(ctx, cf.AccountID)
	if err != nil {
		return fmt.Errorf("can't list tunnels — does the token have Account · Cloudflare Tunnel? %w", err)
	}
	zones, err := api.Zones(ctx, cf.AccountID)
	if err != nil {
		return fmt.Errorf("can't list zones — does the token have Zone · Zone · Read? %w", err)
	}
	if err := secret.Set(cloudflareTokenAccount, tok); err != nil {
		return err
	}
	a.cfg.Cloudflare = &cf
	if err := homecfg.Save(homecfg.DefaultPath(), a.cfg); err != nil {
		return err
	}
	var zn []string
	for _, z := range zones {
		zn = append(zn, z.Name)
	}
	fmt.Printf("✓ saved. The token sees %d tunnels and %d zones (%s).\n", len(tunnels), len(zones), strings.Join(zn, ", "))
	fmt.Println("Next: `home cloud export` writes them to", cf.Dir)
	return nil
}

// defaultCloudDir suggests <git toplevel>/cloud for the current directory.
func defaultCloudDir() string {
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		return filepath.Join(strings.TrimSpace(string(out)), "cloud")
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "cloud")
}

func (a *app) cloudExport(ctx context.Context, dir string) error {
	st, cf, err := a.gather(ctx, cloud.GatherOpts{DNS: true, Nodes: true})
	if err != nil {
		return err
	}
	if dir == "" {
		dir = cf.CloudDir()
	}
	if dir == "" {
		return errors.New("no directory configured — run `home cloud setup` or pass --dir")
	}
	before, _ := cloud.Load(dir)
	fs, notes := cloud.Export(st, before)
	var written []string
	for _, f := range fs.Tunnels {
		p, err := cloud.SaveTunnel(dir, f)
		if err != nil {
			return err
		}
		written = append(written, p)
	}
	for _, z := range fs.Zones {
		p, err := cloud.SaveZone(dir, z)
		if err != nil {
			return err
		}
		written = append(written, p)
	}
	for _, p := range written {
		rel, _ := filepath.Rel(dir, p)
		fmt.Println("✓", rel)
	}
	if before != nil {
		for _, f := range before.Tunnels {
			if st.Tunnel(f.Tunnel) == nil {
				fmt.Printf("⚠ %s: tunnel no longer exists in Cloudflare; file left alone\n", f.Tunnel)
			}
		}
	}
	if len(notes) > 0 {
		fmt.Println("\nWorth a look:")
		for _, n := range notes {
			fmt.Println("  ⚠", n)
		}
	}
	fmt.Println("\nReview with `git diff` and commit;", len(fs.Tunnels), "tunnels and", len(fs.Zones), "zones exported to", dir)
	return nil
}

// ---- home tunnel -------------------------------------------------------------

func (a *app) tunnelCmd(ctx context.Context, args []string) error {
	switch {
	case len(args) == 1 && args[0] == "list", len(args) == 0:
		return a.tunnelList(ctx)
	case len(args) == 2 && args[0] == "show":
		return a.tunnelShow(ctx, args[1])
	case len(args) > 1 && (args[0] == "create" || args[0] == "token" || args[0] == "rotate" || args[0] == "delete"):
		return a.tunnelLifecycle(ctx, args[0], args[1:])
	}
	return errors.New(tunnelUsage)
}

func connSummary(cs []cloudflare.Connector) string {
	if len(cs) == 0 {
		return "0"
	}
	colos := map[string]int{}
	n := 0
	for _, c := range cs {
		for _, k := range c.Conns {
			colos[k.ColoName]++
			n++
		}
	}
	var names []string
	for c := range colos {
		names = append(names, c)
	}
	sort.Strings(names)
	return fmt.Sprintf("%d (%d conns: %s)", len(cs), n, strings.Join(names, ","))
}

func runsOn(rs []cloud.Runner) string {
	if len(rs) == 0 {
		return "-"
	}
	var s []string
	for _, r := range rs {
		x := r.Node + "/" + r.Stack
		if r.State != "running" {
			x += " (" + r.State + ")"
		}
		s = append(s, x)
	}
	return strings.Join(s, ", ")
}

func (a *app) tunnelList(ctx context.Context) error {
	st, cf, err := a.gather(ctx, cloud.GatherOpts{Connectors: true, Nodes: true})
	if err != nil {
		return err
	}
	files, _ := cloud.Load(cf.CloudDir())
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TUNNEL\tSTATUS\tCONNECTORS\tRUNS ON\tROUTES\tFILE")
	for _, t := range st.Tunnels {
		routes := "local config"
		if t.Remote() {
			routes = "0"
			if t.Config != nil {
				f := cloud.Compact(t.Name, t.Config)
				routes = fmt.Sprint(len(f.Hostnames()))
			}
		}
		file := "-"
		if files != nil && files.Tunnel(t.Name) != nil {
			file = "✓"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Status, connSummary(t.Connectors), runsOn(st.Runners[t.ID]), routes, file)
	}
	return w.Flush()
}

func (a *app) tunnelShow(ctx context.Context, name string) error {
	st, _, err := a.gather(ctx, cloud.GatherOpts{Connectors: true, Nodes: true, DNS: true})
	if err != nil {
		return err
	}
	t := st.Tunnel(name)
	if t == nil {
		var names []string
		for _, t := range st.Tunnels {
			names = append(names, t.Name)
		}
		return fmt.Errorf("no tunnel %q (tunnels: %s)", name, strings.Join(names, ", "))
	}
	fmt.Printf("%s  %s\n", t.Name, t.ID)
	src := "remote (Cloudflare)"
	if !t.Remote() {
		src = "local (cloudflared config file)"
	} else if t.Version > 0 {
		src += fmt.Sprintf(", version %d", t.Version)
	}
	fmt.Printf("  status   %s\n  created  %s\n  ingress  %s\n", t.Status, t.CreatedAt.Local().Format("2006-01-02"), src)
	for _, r := range st.Runners[t.ID] {
		h := r.State
		if r.Health != "" {
			h += ", " + r.Health
		}
		fmt.Printf("  runs on  %s (%s)\n", r, h)
	}
	if len(st.Runners[t.ID]) == 0 {
		fmt.Println("  runs on  no cloudflared container found on the nodes")
	}

	fmt.Println("\nConnectors:")
	if len(t.Connectors) == 0 {
		fmt.Println("  none — nothing is serving this tunnel")
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	if len(t.Connectors) > 0 {
		fmt.Fprintln(w, "  ID\tVERSION\tARCH\tUP\tEDGE\tORIGIN IP")
	}
	for _, c := range t.Connectors {
		var edge []string
		ip := ""
		for _, k := range c.Conns {
			e := k.ColoName
			if k.IsPendingReconnect {
				e += "(reconnecting)"
			}
			edge = append(edge, e)
			ip = k.OriginIP
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", shortID(c.ID), c.Version, c.Arch, human(time.Since(c.RunAt)), strings.Join(edge, ","), ip)
	}
	w.Flush()

	if t.Config == nil {
		return nil
	}
	fmt.Println("\nIngress:")
	w = tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "  HOSTNAME\tSERVICE\tORIGIN\tDNS")
	for _, r := range t.Config.Ingress {
		host := r.Hostname + r.Path
		dns := ""
		if r.Hostname == "" {
			host = "(everything else)"
		} else {
			dns = dnsState(st, r.Hostname, t.ID)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", host, r.Service, origin(r), dns)
	}
	return w.Flush()
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// origin summarizes a rule's originRequest; the common host-header + SNI
// pair is shown as "host+sni".
func origin(r cloudflare.IngressRule) string {
	if len(r.OriginRequest) == 0 {
		return "-"
	}
	var parts []string
	keys := make([]string, 0, len(r.OriginRequest))
	for k := range r.OriginRequest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	hh, sn := r.OriginRequest["httpHostHeader"], r.OriginRequest["originServerName"]
	if hh == r.Hostname && sn == r.Hostname {
		parts = append(parts, "host+sni")
	}
	for _, k := range keys {
		v := r.OriginRequest[k]
		if (k == "httpHostHeader" || k == "originServerName") && hh == r.Hostname && sn == r.Hostname {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}

// dnsState describes host's DNS relative to the tunnel that routes it.
func dnsState(st *cloud.State, host, tunnelID string) string {
	if st.ZoneFor(host) == nil {
		return "zone not in account"
	}
	recs := st.Records(host)
	if len(recs) == 0 {
		return "✗ no record"
	}
	for _, r := range recs {
		if id, ok := cloud.TunnelOfRecord(r); ok {
			switch {
			case id != tunnelID:
				other := id
				if t := st.Tunnel(id); t != nil {
					other = t.Name
				}
				return "✗ points at " + other
			case !r.Proxied:
				return "✗ not proxied"
			}
			return "✓"
		}
	}
	return "✗ " + recs[0].Type + " " + recs[0].Content
}

// ---- home route --------------------------------------------------------------

func (a *app) routeCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("route list", flag.ExitOnError)
		all := fs.Bool("all", false, "also show Traefik hosts that no tunnel routes")
		fs.Parse(reorder(args[1:]))
		return a.routeList(ctx, fs.Arg(0), *all)
	case "check":
		return a.routeCheck(ctx, args[1:])
	case "add", "set", "remove", "move":
		return a.routeEdit(ctx, args[0], args[1:])
	}
	return errors.New(routeUsage)
}

type routeRow struct {
	host, tunnel, node, service, dns, traefik, auth string
	notes                                           []string
}

func routeRows(st *cloud.State, all bool) ([]routeRow, int) {
	var rows []routeRow
	routed := map[string]bool{}
	for _, t := range st.Tunnels {
		if t.Config == nil {
			continue
		}
		node := ""
		if rs := st.Runners[t.ID]; len(rs) > 0 {
			node = rs[0].Node
		}
		for _, r := range t.Config.Ingress {
			if r.Hostname == "" {
				continue
			}
			routed[r.Hostname] = true
			row := routeRow{host: r.Hostname + r.Path, tunnel: t.Name, node: node, service: r.Service, dns: dnsState(st, r.Hostname, t.ID)}
			if strings.HasPrefix(row.dns, "✗") {
				row.notes = append(row.notes, "DNS "+strings.TrimPrefix(row.dns, "✗ "))
			}
			if t.Status != "healthy" {
				row.notes = append(row.notes, "tunnel "+t.Status)
			}
			routers := st.RoutersFor(r.Hostname)
			row.traefik, row.auth = routerSummary(routers)
			if len(routers) == 0 && strings.Contains(r.Service, "traefik") && st.Routers != nil {
				row.notes = append(row.notes, "no Traefik router")
			}
			rows = append(rows, row)
		}
	}
	// Tunnel CNAMEs nothing routes.
	for _, z := range st.Zones {
		for _, rec := range z.Records {
			id, ok := cloud.TunnelOfRecord(rec)
			if !ok || routed[rec.Name] {
				continue
			}
			name := id
			if t := st.Tunnel(id); t != nil {
				name = t.Name
			}
			row := routeRow{host: rec.Name, tunnel: name, service: "-", dns: "✓"}
			if st.Tunnel(id) == nil {
				row.notes = append(row.notes, "CNAME to a deleted tunnel")
			} else {
				row.notes = append(row.notes, "CNAME but no ingress rule (gets the catch-all)")
			}
			row.traefik, row.auth = routerSummary(st.RoutersFor(rec.Name))
			routed[rec.Name] = true
			rows = append(rows, row)
		}
	}
	internal := map[string][]cloud.Router{}
	for _, r := range st.Routers {
		if !routed[r.Host] {
			internal[r.Host] = append(internal[r.Host], r)
		}
	}
	if all {
		for h, rs := range internal {
			row := routeRow{host: h, tunnel: "-", node: rs[0].Node, service: "-", dns: "-"}
			row.traefik, row.auth = routerSummary(rs)
			row.notes = []string{"not public (LAN/Tailscale)"}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].host < rows[j].host })
	return rows, len(internal)
}

func routerSummary(rs []cloud.Router) (where, auth string) {
	if len(rs) == 0 {
		return "-", "-"
	}
	seen := map[string]bool{}
	var w []string
	auth = "no"
	for _, r := range rs {
		if k := r.Node + "/" + r.Stack; !seen[k] {
			seen[k] = true
			w = append(w, k)
		}
		if r.Protected() {
			auth = "yes"
		}
	}
	return strings.Join(w, ","), auth
}

func (a *app) routeList(ctx context.Context, filter string, all bool) error {
	st, _, err := a.gather(ctx, cloud.GatherOpts{DNS: true, Nodes: true})
	if err != nil {
		return err
	}
	rows, internal := routeRows(st, all)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tTUNNEL\tSERVICE\tDNS\tTRAEFIK\tAUTH\tNOTES")
	problems := 0
	for _, r := range rows {
		if filter != "" && r.tunnel != filter && r.node != filter && !strings.HasSuffix(r.host, "."+filter) && r.host != filter &&
			!strings.HasPrefix(r.traefik, filter+"/") {
			continue
		}
		dns := r.dns
		if strings.HasPrefix(dns, "✗") {
			dns = "✗"
		}
		if len(r.notes) > 0 && r.tunnel != "-" {
			problems++
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.host, r.tunnel, r.service, dns, r.traefik, r.auth, strings.Join(r.notes, "; "))
	}
	w.Flush()
	if !all && internal > 0 {
		fmt.Printf("\n%d Traefik hosts are not routed by any tunnel (LAN/Tailscale only); --all lists them.\n", internal)
	}
	if problems > 0 {
		fmt.Printf("%d routes need attention; `home route check <host>` tests one end to end.\n", problems)
	}
	return nil
}

type checkRow struct {
	host, dns, ingress, tunnel string
	probe                      cloud.Probe
	problems                   []string
}

func (a *app) routeCheck(ctx context.Context, hosts []string) error {
	st, _, err := a.gather(ctx, cloud.GatherOpts{DNS: true})
	if err != nil {
		return err
	}
	// hostname -> the tunnel whose ingress names it
	ingress := map[string]*cloud.TunnelState{}
	for i := range st.Tunnels {
		t := &st.Tunnels[i]
		if t.Config == nil {
			continue
		}
		for _, r := range t.Config.Ingress {
			if r.Hostname != "" && ingress[r.Hostname] == nil {
				ingress[r.Hostname] = t
			}
		}
	}
	if len(hosts) == 0 {
		for h := range ingress {
			if !strings.HasPrefix(h, "*") {
				hosts = append(hosts, h)
			}
		}
		sort.Strings(hosts)
	}

	rows := make([]checkRow, len(hosts))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, h := range hosts {
		row := &rows[i]
		row.host = h
		// Where DNS sends it.
		var dnsTunnel *cloud.TunnelState
		row.dns = "-"
		for _, rec := range st.Records(h) {
			if id, ok := cloud.TunnelOfRecord(rec); ok {
				dnsTunnel = st.Tunnel(id)
				row.dns = "→ deleted tunnel"
				if dnsTunnel != nil {
					row.dns = "→ " + dnsTunnel.Name
				}
				if !rec.Proxied {
					row.problems = append(row.problems, "tunnel CNAME is not proxied")
				}
				break
			}
			row.dns = rec.Type + " " + rec.Content
		}
		switch {
		case st.ZoneFor(h) == nil:
			row.dns = "(zone not in account)"
		case row.dns == "-":
			row.problems = append(row.problems, "no DNS record")
		case dnsTunnel == nil && strings.HasPrefix(row.dns, "→"):
			row.problems = append(row.problems, "DNS points at a deleted tunnel")
		}
		// Whether that tunnel routes it.
		row.ingress, row.tunnel = "-", "-"
		if t := ingress[h]; t != nil {
			row.ingress = "✓ " + t.Name
		}
		if dnsTunnel != nil {
			row.tunnel = dnsTunnel.Status
			if dnsTunnel.Status != "healthy" {
				row.problems = append(row.problems, "tunnel "+dnsTunnel.Name+" is "+dnsTunnel.Status)
			}
			if t := ingress[h]; t == nil || t.ID != dnsTunnel.ID {
				row.problems = append(row.problems, "tunnel "+dnsTunnel.Name+" has no ingress rule for it")
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			row.probe = cloud.ProbeHost(ctx, h)
		}()
	}
	wg.Wait()

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "\tHOST\tDNS\tINGRESS\tTUNNEL\tHTTP\tTIME\tPROBLEM")
	failed, warned := 0, 0
	for _, r := range rows {
		if p := r.probe.Problem(); p != "" {
			r.problems = append(r.problems, p)
		}
		icon := "✓"
		switch {
		case len(r.problems) > 0:
			icon = "✗"
			failed++
		case r.probe.Warning() != "":
			icon = "⚠"
			warned++
			r.problems = append(r.problems, r.probe.Warning())
		}
		lat := "-"
		if r.probe.Err == nil {
			lat = fmt.Sprintf("%dms", r.probe.Latency.Milliseconds())
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", icon, r.host, r.dns, r.ingress, r.tunnel, r.probe.Result(), lat, strings.Join(r.problems, "; "))
	}
	w.Flush()
	if failed > 0 {
		return fmt.Errorf("%d of %d routes failed", failed, len(rows))
	}
	fmt.Printf("\n✓ all %d routes answer through Cloudflare", len(rows))
	if warned > 0 {
		fmt.Printf(" (%d with a warning)", warned)
	}
	fmt.Println()
	return nil
}

// cloudPlan compares the files with Cloudflare and, with apply, makes
// Cloudflare match after a confirmation and a snapshot. It returns the number
// of changes.
func (a *app) cloudPlan(ctx context.Context, apply, yes bool) (int, error) {
	st, cf, err := a.gather(ctx, cloud.GatherOpts{DNS: true})
	if err != nil {
		return 0, err
	}
	files, err := cloud.Load(cf.CloudDir())
	if err != nil {
		return 0, err
	}
	if len(files.Tunnels) == 0 && len(files.Zones) == 0 {
		return 0, fmt.Errorf("no tunnel or zone files in %s — run `home cloud export` first", cf.CloudDir())
	}
	d, notes, err := cloud.FromFiles(files, st)
	if err != nil {
		return 0, err
	}
	changes := cloud.Plan(d, st)
	for _, n := range notes {
		fmt.Println("·", n)
	}
	if len(changes) == 0 {
		fmt.Println("✓ Cloudflare matches", cf.CloudDir())
		return 0, nil
	}
	return len(changes), a.applyChanges(ctx, st, cf, changes, apply, yes)
}

func printCloudPlan(changes []cloud.Change) {
	fmt.Println("Plan:")
	for _, c := range changes {
		if c.Config != nil && len(c.Lines) == 1 && c.Lines[0] == "(drop the removed rules)" {
			continue // second step of a change already shown
		}
		fmt.Println(" ", c)
	}
}

func (a *app) applyChanges(ctx context.Context, st *cloud.State, cf *homecfg.Cloudflare, changes []cloud.Change, apply, yes bool) error {
	printCloudPlan(changes)
	if !apply {
		fmt.Println("\n`home cloud apply` makes these changes.")
		return nil
	}
	if err := confirm(yes, fmt.Sprintf("Apply %d changes to Cloudflare?", len(changes))); err != nil {
		return err
	}
	api, _, err := a.cloudAPI()
	if err != nil {
		return err
	}
	snap := cloud.TakeSnapshot(st, changes)
	p, err := cloud.SaveSnapshot(homecfg.StateDir(), snap)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := cloud.Apply(ctx, api, cf.AccountID, changes, logf); err != nil {
		return fmt.Errorf("%w\nthe changes before it were applied; `home cloud undo` restores the state from before this apply", err)
	}
	fmt.Println("✓ applied; snapshot of the previous state:", filepath.Base(p), "(`home cloud undo` restores it)")
	return nil
}

func (a *app) cloudUndo(ctx context.Context, list, yes bool, which string) error {
	snaps := cloud.Snapshots(homecfg.StateDir())
	if list {
		for i, p := range snaps {
			s, err := cloud.LoadSnapshot(p)
			if err != nil {
				continue
			}
			var what []string
			for _, t := range s.Tunnels {
				what = append(what, "tunnel "+t.Name)
			}
			for _, z := range s.Zones {
				what = append(what, "dns "+z.Name)
			}
			fmt.Printf("%2d  %s  %s  %s\n", i+1, filepath.Base(p), s.Time.Local().Format("2006-01-02 15:04"), strings.Join(what, ", "))
		}
		return nil
	}
	if len(snaps) == 0 {
		return errors.New("no snapshots yet — `home cloud apply` saves one before every change")
	}
	path := snaps[0]
	if which != "" {
		path = filepath.Join(cloud.SnapshotDir(homecfg.StateDir()), strings.TrimSuffix(which, ".json")+".json")
	}
	s, err := cloud.LoadSnapshot(path)
	if err != nil {
		return err
	}
	st, cf, err := a.gather(ctx, cloud.GatherOpts{DNS: true})
	if err != nil {
		return err
	}
	files, _ := cloud.Load(cf.CloudDir())
	d := s.Desired(func(zone string) []string {
		if files != nil {
			if z := files.Zone(zone); z != nil {
				return z.Ignore
			}
		}
		return nil
	})
	changes := cloud.Plan(d, st)
	fmt.Println("Restoring the state from", s.Time.Local().Format("2006-01-02 15:04"), "("+filepath.Base(path)+")")
	if len(changes) == 0 {
		fmt.Println("✓ Cloudflare already matches it")
		return nil
	}
	if err := a.applyChanges(ctx, st, cf, changes, true, yes); err != nil {
		return err
	}
	fmt.Println("The files no longer match Cloudflare: `git checkout` them, or `home cloud export` to take this state.")
	return nil
}

// cloudFindings is Cloudflare's part of `home doctor` and the daily check:
// drift between the files and Cloudflare, connectors that are down, and
// routes without DNS or a router.
func (a *app) cloudFindings(ctx context.Context) ([]string, error) {
	api, cf, err := a.cloudAPI()
	if err != nil {
		return nil, err
	}
	st, err := cloud.Gather(ctx, api, cf.AccountID, a.cfg, a.r, cloud.GatherOpts{DNS: true, Nodes: true})
	if err != nil {
		return nil, err
	}
	var items []string
	files, err := cloud.Load(cf.CloudDir())
	if err != nil {
		items = append(items, err.Error())
	} else if len(files.Tunnels)+len(files.Zones) > 0 {
		d, _, err := cloud.FromFiles(files, st)
		if err != nil {
			items = append(items, err.Error())
		} else if n := len(cloud.Plan(d, st)); n > 0 {
			items = append(items, fmt.Sprintf("%d differences between %s and Cloudflare — `home cloud plan`", n, filepath.Base(cf.CloudDir())))
		}
	}
	for _, t := range st.Tunnels {
		// Only tunnels a node is meant to run: a laptop's tunnel sleeps.
		meant := len(st.Runners[t.ID]) > 0
		if files != nil {
			if f := files.Tunnel(t.Name); f != nil && f.Node != "" {
				meant = true
			}
		}
		if meant && t.Status != "healthy" {
			items = append(items, fmt.Sprintf("tunnel %s is %s", t.Name, t.Status))
		}
	}
	rows, _ := routeRows(st, false)
	for _, r := range rows {
		if len(r.notes) > 0 && r.tunnel != "-" {
			items = append(items, r.host+": "+strings.Join(r.notes, "; "))
		}
	}
	return items, nil
}
