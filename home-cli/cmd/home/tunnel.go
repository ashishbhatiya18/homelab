package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ashishbhatiya18/home/internal/cloud"
	"github.com/ashishbhatiya18/home/internal/cloudflare"
	"github.com/ashishbhatiya18/home/internal/remote"
	"github.com/ashishbhatiya18/home/internal/stack"
)

// Tunnel lifecycle. Tokens go from the API straight to the node over SSH
// stdin: they are never printed (unless asked) or written on the Mac.

func (a *app) tunnelLifecycle(ctx context.Context, action string, args []string) error {
	fl := flag.NewFlagSet("tunnel "+action, flag.ExitOnError)
	node := fl.String("node", "", "node that runs the connector")
	stk := fl.String("stack", "", "stack that runs the connector")
	ctr := fl.String("container", "", "connector container name")
	secretPath := fl.String("secret", "", "node path of the token file (just the token, or an env file with TUNNEL_TOKEN=)")
	service := fl.String("service", "https://traefik:443", "default origin service for its routes")
	install := fl.Bool("install", false, "write the token to the node and recreate the connector")
	show := fl.Bool("print", false, "print the token")
	yes := fl.Bool("yes", false, "do not ask for confirmation")
	fl.Parse(reorderFlags(args, "node", "stack", "container", "secret", "service"))
	if fl.NArg() != 1 {
		return errors.New(tunnelUsage)
	}
	name := fl.Arg(0)
	api, cf, err := a.cloudAPI()
	if err != nil {
		return err
	}
	dir := cf.CloudDir()
	files, err := cloud.Load(dir)
	if err != nil {
		return err
	}
	f := files.Tunnel(name)

	switch action {
	case "create":
		return a.tunnelCreate(ctx, api, cf.AccountID, dir, files, name, *node, *stk, *ctr, *secretPath, *service)
	}

	tunnels, err := api.Tunnels(ctx, cf.AccountID)
	if err != nil {
		return err
	}
	var t *cloudflare.Tunnel
	for i := range tunnels {
		if tunnels[i].Name == name || tunnels[i].ID == name {
			t = &tunnels[i]
		}
	}
	if t == nil {
		return fmt.Errorf("no tunnel %q in Cloudflare", name)
	}
	if f == nil {
		f = files.Tunnel(t.Name)
	}
	if *secretPath != "" && f != nil {
		f.Secret = *secretPath
		if err := cloud.SetField(dir, f.Tunnel, "secret", *secretPath); err != nil {
			return err
		}
	}

	switch action {
	case "token":
		if *show {
			tok, err := api.TunnelToken(ctx, cf.AccountID, t.ID)
			if err != nil {
				return err
			}
			fmt.Println(tok)
			return nil
		}
		if !*install {
			return errors.New("pass --install to write the token to the node (or --print to show it)")
		}
		if err := a.checkInstallable(ctx, f); err != nil {
			return err
		}
		tok, err := api.TunnelToken(ctx, cf.AccountID, t.ID)
		if err != nil {
			return err
		}
		return a.installToken(ctx, api, cf.AccountID, t, f, tok)
	case "rotate":
		if err := a.checkInstallable(ctx, f); err != nil {
			return err
		}
		if err := confirm(*yes, fmt.Sprintf("Rotate the secret of %s? Its connector reconnects with the new token on %s.", t.Name, f.Node)); err != nil {
			return err
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		if err := api.RotateTunnelSecret(ctx, cf.AccountID, t.ID, base64.StdEncoding.EncodeToString(b)); err != nil {
			return err
		}
		logf("new tunnel secret set")
		tok, err := api.TunnelToken(ctx, cf.AccountID, t.ID)
		if err != nil {
			return fmt.Errorf("%w — the old token no longer works; retry with `home tunnel token %s --install`", err, t.Name)
		}
		if err := a.installToken(ctx, api, cf.AccountID, t, f, tok); err != nil {
			return fmt.Errorf("%w — retry with `home tunnel token %s --install`", err, t.Name)
		}
		return nil
	case "delete":
		return a.tunnelDelete(ctx, api, cf.AccountID, dir, t, f, *yes)
	}
	return errors.New(tunnelUsage)
}

// checkInstallable makes sure the token has somewhere to go before anything
// irreversible happens.
func (a *app) checkInstallable(ctx context.Context, f *cloud.TunnelFile) error {
	if f == nil {
		return errors.New("the tunnel has no file — `home cloud export` first")
	}
	if f.Node == "" || f.Stack == "" || f.Container == "" {
		return fmt.Errorf("tunnels/%s.yaml needs node, stack and container", f.Tunnel)
	}
	if f.Secret == "" {
		return fmt.Errorf("where does %s read its token? pass --secret <node path> once (it is saved in the file)", f.Container)
	}
	if _, err := a.cfg.Node(f.Node); err != nil {
		return err
	}
	cmd := fmt.Sprintf(`f=%[1]s; if [ -e "$f" ]; then [ -w "$f" ]; else [ -w "$(dirname "$f")" ]; fi`, remote.Quote(f.Secret))
	if err := a.r.Run(ctx, f.Node, cmd, nil, nil); err != nil {
		return fmt.Errorf("%s: can't write %s: %w", f.Node, f.Secret, err)
	}
	return nil
}

// tokenScript writes the token from stdin in place (same inode, so a bind
// mount of the single file sees it): replacing the TUNNEL_TOKEN= line of an
// env file, or the whole file otherwise.
const tokenScript = `set -e
f=%[1]s
umask 077
tok=$(cat)
if [ -f "$f" ] && grep -q '^TUNNEL_TOKEN=' "$f"; then
  tmp=$(mktemp)
  T="$tok" awk '/^TUNNEL_TOKEN=/ {print "TUNNEL_TOKEN=" ENVIRON["T"]; next} {print}' "$f" > "$tmp"
  cat "$tmp" > "$f"
  rm -f "$tmp"
else
  printf '%%s' "$tok" > "$f"
fi
`

func (a *app) installToken(ctx context.Context, api *cloudflare.Client, accountID string, t *cloudflare.Tunnel, f *cloud.TunnelFile, tok string) error {
	n, err := a.cfg.Node(f.Node)
	if err != nil {
		return err
	}
	if err := a.r.Run(ctx, f.Node, fmt.Sprintf(tokenScript, remote.Quote(f.Secret)), strings.NewReader(tok), nil); err != nil {
		return fmt.Errorf("writing the token: %w", err)
	}
	logf("token written to %s:%s", f.Node, f.Secret)
	svc, err := a.r.Output(ctx, f.Node, "docker inspect -f '{{index .Config.Labels \"com.docker.compose.service\"}}' "+remote.Quote(f.Container))
	if svc = strings.TrimSpace(svc); err != nil || svc == "" {
		return fmt.Errorf("token written; container %s not found on %s — start the %s stack to use it", f.Container, f.Node, f.Stack)
	}
	started := time.Now().Add(-5 * time.Second)
	if err := a.r.Run(ctx, f.Node, stack.ComposeCmd(n.StacksDir, f.Stack, "up", "-d", "--force-recreate", "--no-deps", svc), nil, os.Stdout); err != nil {
		return fmt.Errorf("recreating %s: %w", f.Container, err)
	}
	logf("waiting for %s to connect…", t.Name)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		cs, err := api.Connectors(ctx, accountID, t.ID)
		if err == nil {
			for _, c := range cs {
				if c.RunAt.After(started) && len(c.Conns) > 0 {
					fmt.Printf("✓ %s connected (%d connections, cloudflared %s)\n", t.Name, len(c.Conns), c.Version)
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("%s did not connect within 90s — `home stack logs %s/%s %s`", t.Name, f.Node, f.Stack, svc)
}

func (a *app) tunnelCreate(ctx context.Context, api *cloudflare.Client, accountID, dir string, files *cloud.Files, name, node, stk, ctr, secretPath, service string) error {
	if files.Tunnel(name) != nil {
		return fmt.Errorf("tunnels/%s.yaml already exists", name)
	}
	if node == "" || stk == "" {
		return errors.New("usage: home tunnel create <name> --node <node> --stack <stack> [--container c] [--secret path] [--service url]")
	}
	if _, err := a.cfg.Node(node); err != nil {
		return err
	}
	tunnels, err := api.Tunnels(ctx, accountID)
	if err != nil {
		return err
	}
	for _, t := range tunnels {
		if t.Name == name {
			return fmt.Errorf("a tunnel named %s already exists — `home cloud export` to add its file", name)
		}
	}
	if ctr == "" {
		ctr = stk + "-cloudflared"
	}
	if secretPath == "" {
		// <base>/secrets next to <base>/stacks, the layout home deploy uses
		n, _ := a.cfg.Node(node)
		base, ok := strings.CutSuffix(n.StacksDir, "/stacks")
		if !ok {
			return errors.New("pass --secret <node path> for the token file")
		}
		secretPath = base + "/secrets/" + strings.ReplaceAll(name, ".", "_") + "_tunnel_token"
	}
	t, err := api.CreateTunnel(ctx, accountID, name)
	if err != nil {
		return err
	}
	logf("created tunnel %s (%s)", t.Name, t.ID)
	f := cloud.TunnelFile{Tunnel: name, ID: t.ID, Node: node, Stack: stk, Container: ctr, Secret: secretPath,
		Defaults: &cloud.Defaults{Service: service},
		Ingress:  []cloud.Rule{{Service: "http_status:404"}}}
	if strings.HasPrefix(service, "https://") {
		f.Defaults.OriginRequest = map[string]any{"httpHostHeader": cloud.HostVar, "originServerName": cloud.HostVar}
	}
	cfg, _ := f.Expand()
	if err := api.PutTunnelConfig(ctx, accountID, t.ID, cfg); err != nil {
		return err
	}
	p, err := cloud.SaveTunnel(dir, f)
	if err != nil {
		return err
	}
	fmt.Println("✓ wrote", relPath(dir, p))
	if err := a.checkInstallable(ctx, &f); err == nil {
		tok, err := api.TunnelToken(ctx, accountID, t.ID)
		if err != nil {
			return err
		}
		if err := a.r.Run(ctx, node, fmt.Sprintf(tokenScript, remote.Quote(secretPath)), strings.NewReader(tok), nil); err != nil {
			return fmt.Errorf("writing the token: %w", err)
		}
		logf("token written to %s:%s", node, secretPath)
	}
	secretName := strings.ReplaceAll(name, ".", "_") + "_tunnel_token"
	fmt.Printf(`
Next, add the connector to nodes/%[1]s/%[2]s/compose.yaml and deploy:

  services:
    cloudflared:
      image: cloudflare/cloudflared:latest
      container_name: %[3]s
      command: tunnel --no-autoupdate run
      restart: always
      environment:
        - TUNNEL_TOKEN_FILE=/run/secrets/%[4]s
      secrets:
        - %[4]s
      networks:
        - internal_bridge
  secrets:
    %[4]s:
      file: %[5]s

Then route hostnames through it: home route add <host> --tunnel %[6]s
`, node, stk, ctr, secretName, secretPath, name)
	return nil
}

func (a *app) tunnelDelete(ctx context.Context, api *cloudflare.Client, accountID, dir string, t *cloudflare.Tunnel, f *cloud.TunnelFile, yes bool) error {
	st, _, err := a.gather(ctx, cloud.GatherOpts{DNS: true, Nodes: true})
	if err != nil {
		return err
	}
	var blockers []string
	if ts := st.Tunnel(t.ID); ts != nil && ts.Config != nil {
		for _, r := range ts.Config.Ingress {
			if r.Hostname != "" {
				blockers = append(blockers, "route "+r.Hostname+" (`home route remove "+r.Hostname+"` or `move`)")
			}
		}
	}
	for _, z := range st.Zones {
		for _, r := range z.Records {
			if id, ok := cloud.TunnelOfRecord(r); ok && id == t.ID {
				blockers = append(blockers, "DNS record "+r.Name)
			}
		}
	}
	for _, r := range st.Runners[t.ID] {
		if r.State == "running" {
			blockers = append(blockers, fmt.Sprintf("connector %s is running — remove it from the %s stack and deploy", r, r.Stack))
		}
	}
	if len(blockers) > 0 {
		return fmt.Errorf("%s is still in use:\n  %s", t.Name, strings.Join(blockers, "\n  "))
	}
	if err := confirm(yes, "Delete tunnel "+t.Name+"? This cannot be undone."); err != nil {
		return err
	}
	if err := api.CleanupConnections(ctx, accountID, t.ID); err != nil {
		logf("cleanup of stale connections: %v", err)
	}
	if err := api.DeleteTunnel(ctx, accountID, t.ID); err != nil {
		return err
	}
	logf("deleted tunnel %s", t.Name)
	if f != nil {
		p := cloud.TunnelPath(dir, f.Tunnel)
		if err := os.Remove(p); err == nil {
			fmt.Println("✓ removed", relPath(dir, p))
		}
		if f.Secret != "" {
			fmt.Printf("The token file %s:%s is no longer used; delete it when convenient.\n", f.Node, f.Secret)
		}
	}
	return nil
}
