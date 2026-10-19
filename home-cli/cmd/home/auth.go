package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashishbhatiya18/home/internal/auth"
	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/remote"
	"github.com/ashishbhatiya18/home/internal/stack"
)

const authUsage = `usage:
  home auth user list
  home auth user add <name> --email E [--name "Display Name"] [--groups a,b]
  home auth user set <name> [--email E] [--name N] [--groups a,b]
  home auth user password <name>              new random password, shown once
  home auth user disable|enable <name>
  home auth user passkeys <name> [--rm <description>]
  home auth user rm <name> [--yes]
  home auth client list
  home auth client show <id>
  home auth client add <id> --redirect URL [--redirect URL…] [--name N] [--policy P]
                     [--scopes a,b] [--pkce] [--public] [--consent M] [--auth-method M] [--yes]
  home auth client set <id> [--redirect URL…] [--name N] [--policy P] [--scopes a,b]
                     [--pkce=true|false] [--consent M] [--auth-method M] [--yes]
  home auth client rotate <id> [--yes]        new secret, shown once
  home auth client rm <id> [--yes]
  home auth code [--wait]                     the latest one-time code (--wait: the next one)`

// authEnv is the Authelia instance `home auth` manages (config.yaml auth:).
type authEnv struct {
	*app
	c    homecfg.Auth
	node *homecfg.Node
}

func (a *app) authEnv() (*authEnv, error) {
	if a.cfg.Auth == nil || a.cfg.Auth.Node == "" {
		return nil, fmt.Errorf("no Authelia configured — add to %s:\n\n  auth:\n    node: <node running Authelia>\n    url: https://<authelia host>\n\n(stack, container, secrets and data directories have defaults; see `home help`)", homecfg.DefaultPath())
	}
	n, err := a.cfg.Node(a.cfg.Auth.Node)
	if err != nil {
		return nil, err
	}
	return &authEnv{app: a, c: a.cfg.Auth.Resolved(n), node: n}, nil
}

func (a *app) authCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(authUsage)
	}
	e, err := a.authEnv()
	if err != nil {
		return err
	}
	switch args[0] {
	case "user", "users":
		return e.userCmd(ctx, args[1:])
	case "client", "clients":
		return e.clientCmd(ctx, args[1:])
	case "code":
		return e.code(ctx, args[1:])
	}
	return errors.New(authUsage)
}

// ── node access ─────────────────────────────────────────────────────────────

func (e *authEnv) path(name string) string { return e.c.Dir + "/" + name }

// read returns a file from Authelia's directory on the node and whether it
// exists.
func (e *authEnv) read(ctx context.Context, name string) (string, bool, error) {
	out, err := e.r.Output(ctx, e.node.Name, fmt.Sprintf(`if [ -e %[1]s ]; then printf 1; cat %[1]s; else printf 0; fi`, remote.Quote(e.path(name))))
	if err != nil {
		return "", false, err
	}
	if out == "" {
		return "", false, errors.New("no answer from the node")
	}
	return out[1:], out[0] == '1', nil
}

// authelia runs the authelia CLI in the running container.
func (e *authEnv) authelia(ctx context.Context, args ...string) (string, error) {
	q := make([]string, len(args))
	for i, s := range args {
		q[i] = remote.Quote(s)
	}
	return e.r.Output(ctx, e.node.Name, "docker exec "+remote.Quote(e.c.Container)+" authelia "+strings.Join(q, " ")+" 2>&1")
}

// newSecret has Authelia generate a random secret and its digest (argon2 for
// passwords, pbkdf2-sha512 for client secrets), so the plaintext is never on a
// command line.
func (e *authEnv) newSecret(ctx context.Context, kind string) (secret, digest string, err error) {
	args := []string{"crypto", "hash", "generate", "argon2", "--random", "--random.length", "32", "--random.charset", "alphanumeric"}
	if kind == "pbkdf2" {
		args = []string{"crypto", "hash", "generate", "pbkdf2", "--variant", "sha512", "--random", "--random.length", "64", "--random.charset", "alphanumeric"}
	}
	out, err := e.authelia(context.WithoutCancel(ctx), args...)
	if err != nil {
		return "", "", err
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, "Random Password: "); ok {
			secret = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(l, "Digest: "); ok {
			digest = strings.TrimSpace(v)
		}
	}
	if secret == "" || digest == "" {
		return "", "", fmt.Errorf("unexpected output from authelia crypto hash generate: %s", strings.TrimSpace(out))
	}
	return secret, digest, nil
}

// writeFile replaces a file in Authelia's directory atomically, mode 600.
func (e *authEnv) writeFile(ctx context.Context, name, content string) error {
	return e.r.Run(ctx, e.node.Name, fmt.Sprintf(`set -e
f=%s; t="$f.tmp"
install -m 600 /dev/null "$t"
cat > "$t"
mv "$t" "$f"`, remote.Quote(e.path(name))), strings.NewReader(content), nil)
}

func (e *authEnv) sh(ctx context.Context, cmd string) error {
	return e.r.Run(context.WithoutCancel(ctx), e.node.Name, cmd, nil, nil)
}

func (e *authEnv) restartNote() string {
	return fmt.Sprintf("Authelia on %s restarts to apply this; unless its sessions are in Redis, everyone is signed out.", e.node.Name)
}

// ── users ───────────────────────────────────────────────────────────────────

func (e *authEnv) loadUsers(ctx context.Context) (*auth.Users, string, error) {
	raw, ok, err := e.read(ctx, "users.yml")
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", fmt.Errorf("%s:%s does not exist (seed it with scripts/authelia-secrets.sh)", e.node.Name, e.path("users.yml"))
	}
	u, err := auth.ParseUsers([]byte(raw))
	return u, raw, err
}

// saveUsers writes users.yml in place and waits for Authelia's file watcher
// to reload it; when Authelia rejects it, the previous version goes back.
func (e *authEnv) saveUsers(ctx context.Context, before string, u *auth.Users) error {
	since, err := e.r.Output(ctx, e.node.Name, "date +%s.%N")
	if err != nil {
		return err
	}
	since = strings.TrimSpace(since)
	after := u.Bytes()
	file := e.path("users.yml")
	if err := putNodeFile(ctx, e.r, e.node.Name, file, false, true, before, after); err != nil {
		return err
	}
	logs := fmt.Sprintf("docker logs --since %s %s 2>&1 | grep 'watcher=users' || true", since, remote.Quote(e.c.Container))
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		out, err := e.r.Output(ctx, e.node.Name, logs)
		if err != nil {
			return err
		}
		for _, l := range strings.Split(out, "\n") {
			switch {
			case strings.Contains(l, "Reloaded successfully"):
				return nil
			case strings.Contains(l, "level=error"):
				if rerr := putNodeFile(context.WithoutCancel(ctx), e.r, e.node.Name, file, false, true, string(after), []byte(before)); rerr != nil {
					return fmt.Errorf("Authelia rejected users.yml (%s), and restoring the previous version failed: %v", l, rerr)
				}
				return fmt.Errorf("Authelia rejected users.yml (%s); the previous version is back", l)
			}
		}
	}
	return fmt.Errorf("users.yml saved, but Authelia did not confirm reloading it within 10s — see `home stack logs %s/%s %s`", e.node.Name, e.c.Stack, e.c.Service)
}

func (e *authEnv) passkeys(ctx context.Context) ([]auth.Passkey, error) {
	out, err := e.authelia(ctx, "storage", "user", "webauthn", "list")
	if err != nil {
		return nil, err
	}
	return auth.ParsePasskeys(out), nil
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (e *authEnv) userCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(authUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("auth user "+sub, flag.ExitOnError)
	email := fs.String("email", "", "email address (apps match accounts on it)")
	name := fs.String("name", "", "display name")
	groups := fs.String("groups", "", "comma-separated groups (access rules and OIDC policies use them)")
	rm := fs.String("rm", "", "passkeys: delete the passkey with this description")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	fs.Parse(reorderFlags(args[1:], "email", "name", "groups", "rm"))
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	if sub == "list" {
		return e.listUsers(ctx)
	}
	if fs.NArg() != 1 {
		return errors.New(authUsage)
	}
	user := fs.Arg(0)
	u, before, err := e.loadUsers(ctx)
	if err != nil {
		return err
	}
	_, exists := u.Get(user)
	if sub != "add" && !exists {
		return fmt.Errorf("no user %q (see `home auth user list`)", user)
	}

	switch sub {
	case "add":
		pw, digest, err := e.newSecret(ctx, "argon2")
		if err != nil {
			return err
		}
		if err := u.Add(auth.User{Name: user, DisplayName: *name, Email: *email, Groups: splitList(*groups)}, digest); err != nil {
			return err
		}
		if err := e.saveUsers(ctx, before, u); err != nil {
			return err
		}
		fmt.Printf("✓ user %s added\n  first-login password  %s   (shown once)\n", user, pw)
		fmt.Printf("\nThey sign in at %s with it, then add passkeys under Two-Factor Authentication →\nWebAuthn Credentials; the one-time code it asks for: `home auth code --wait`.\n", e.issuer())
		return nil

	case "set":
		var c auth.Change
		if given["email"] {
			c.Email = email
		}
		if given["name"] {
			c.DisplayName = name
		}
		if given["groups"] {
			g := splitList(*groups)
			c.Groups = &g
		}
		if c == (auth.Change{}) {
			return errors.New("nothing to change: give --email, --name or --groups")
		}
		if err := u.Set(user, c); err != nil {
			return err
		}
		if err := e.saveUsers(ctx, before, u); err != nil {
			return err
		}
		fmt.Printf("✓ user %s updated (apps see new claims at their next sign-in)\n", user)
		return nil

	case "password":
		pw, digest, err := e.newSecret(ctx, "argon2")
		if err != nil {
			return err
		}
		if err := u.Set(user, auth.Change{Password: &digest}); err != nil {
			return err
		}
		if err := e.saveUsers(ctx, before, u); err != nil {
			return err
		}
		fmt.Printf("✓ new password for %s: %s   (shown once)\n", user, pw)
		return nil

	case "disable", "enable":
		off := sub == "disable"
		if err := u.Set(user, auth.Change{Disabled: &off}); err != nil {
			return err
		}
		if err := e.saveUsers(ctx, before, u); err != nil {
			return err
		}
		if off {
			fmt.Printf("✓ %s disabled: no new sign-ins; open sessions end at Authelia's next user refresh (≤5 min)\n", user)
		} else {
			fmt.Printf("✓ %s enabled\n", user)
		}
		return nil

	case "passkeys":
		ps, err := e.passkeys(ctx)
		if err != nil {
			return err
		}
		var mine []auth.Passkey
		for _, p := range ps {
			if p.Username == user {
				mine = append(mine, p)
			}
		}
		if given["rm"] {
			found := false
			for _, p := range mine {
				found = found || p.Description == *rm
			}
			if !found {
				return fmt.Errorf("%s has no passkey %q", user, *rm)
			}
			if out, err := e.authelia(ctx, "storage", "user", "webauthn", "delete", user, "--description", *rm); err != nil {
				return fmt.Errorf("%w: %s", err, out)
			}
			fmt.Printf("✓ passkey %q of %s deleted\n", *rm, user)
			return nil
		}
		if len(mine) == 0 {
			fmt.Printf("%s has no passkeys.\n", user)
			return nil
		}
		for _, p := range mine {
			fmt.Printf("  %s  (%s)\n", p.Description, p.RPID)
		}
		return nil

	case "rm", "remove", "delete":
		if err := confirm(*yes, fmt.Sprintf("Delete user %s and their passkeys? Do not reuse the name later: Authelia keeps its OIDC subject, so apps would give a new %s this account. `home auth user disable` keeps it instead.", user, user)); err != nil {
			return err
		}
		if err := u.Remove(user); err != nil {
			return err
		}
		ps, perr := e.passkeys(ctx)
		if err := e.saveUsers(ctx, before, u); err != nil {
			return err
		}
		n := 0
		for _, p := range ps {
			if p.Username == user {
				n++
			}
		}
		if perr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not list passkeys (%v); delete them with `authelia storage user webauthn delete %s --all`\n", perr, user)
		} else if n > 0 {
			if out, err := e.authelia(ctx, "storage", "user", "webauthn", "delete", user, "--all"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: deleting %s's passkeys: %v: %s\n", user, err, strings.TrimSpace(out))
			}
		}
		fmt.Printf("✓ user %s deleted; their accounts inside apps (e.g. Immich) are not touched\n", user)
		return nil
	}
	return errors.New(authUsage)
}

func (e *authEnv) listUsers(ctx context.Context) error {
	u, _, err := e.loadUsers(ctx)
	if err != nil {
		return err
	}
	ps, perr := e.passkeys(ctx)
	count := map[string]int{}
	for _, p := range ps {
		count[p.Username]++
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USER\tNAME\tEMAIL\tGROUPS\tPASSKEYS\tSTATUS")
	for _, x := range u.List() {
		st, pk := "active", fmt.Sprint(count[x.Name])
		if x.Disabled {
			st = "disabled"
		}
		if perr != nil {
			pk = "?"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", x.Name, x.DisplayName, x.Email, strings.Join(x.Groups, ","), pk, st)
	}
	w.Flush()
	if perr != nil {
		fmt.Fprintln(os.Stderr, "warning: passkeys:", perr)
	}
	return nil
}

// ── clients ─────────────────────────────────────────────────────────────────

func (e *authEnv) issuer() string {
	if e.c.URL != "" {
		return strings.TrimSuffix(e.c.URL, "/")
	}
	return "the Authelia portal"
}

func (e *authEnv) loadClients(ctx context.Context) (*auth.Clients, error) {
	raw, _, err := e.read(ctx, "clients.yml")
	if err != nil {
		return nil, err
	}
	return auth.ParseClients([]byte(raw))
}

// configFiles is the container's configuration file list (X_AUTHELIA_CONFIG),
// which must include clients.yml for changes to it to mean anything.
func (e *authEnv) configFiles(ctx context.Context) ([]string, error) {
	out, err := e.r.Output(ctx, e.node.Name, "docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "+remote.Quote(e.c.Container))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, "X_AUTHELIA_CONFIG="); ok {
			files = splitList(v)
		}
	}
	want := e.c.Mount + "/clients.yml"
	for _, f := range files {
		if f == want {
			return files, nil
		}
	}
	return nil, fmt.Errorf("Authelia does not load %s: set X_AUTHELIA_CONFIG=/config/configuration.yml,%s in its compose environment (currently %q)", want, want, strings.Join(files, ","))
}

// applyClients installs next as clients.yml (nil: unchanged, e.g. after a
// secret rotation), validated by Authelia itself first, then restarts
// Authelia and waits for it to be healthy. Any failure after the install
// puts the previous clients.yml back, runs undo and restarts again.
func (e *authEnv) applyClients(ctx context.Context, next *auth.Clients, undo func() error) error {
	files, err := e.configFiles(ctx)
	if err != nil {
		return err
	}
	cfgArgs := []string{"validate-config"}
	for _, f := range files {
		if next != nil && f == e.c.Mount+"/clients.yml" {
			f += ".new"
		}
		cfgArgs = append(cfgArgs, "--config", f)
	}
	if next != nil {
		if err := e.writeFile(ctx, "clients.yml.new", string(next.Bytes())); err != nil {
			return err
		}
	}
	out, err := e.authelia(ctx, cfgArgs...)
	if err != nil || !strings.Contains(out, "successfully") {
		e.sh(ctx, "rm -f "+remote.Quote(e.path("clients.yml.new")))
		if undo != nil {
			undo()
		}
		return fmt.Errorf("Authelia rejects the change, nothing applied:\n%s", validationErrors(out))
	}
	f := remote.Quote(e.path("clients.yml"))
	if next != nil {
		if err := e.sh(ctx, fmt.Sprintf(`set -e; f=%s; if [ -e "$f" ]; then cp -p "$f" "$f.prev"; else rm -f "$f.prev"; fi; mv "$f.new" "$f"`, f)); err != nil {
			return err
		}
	}
	st := stack.Stack{Node: e.node.Name, Name: e.c.Stack, Dir: e.node.StacksDir}
	logf("%s: restarting %s", e.node.Name, e.c.Service)
	err = e.sm.Restart(ctx, st, []string{e.c.Service}, io.Discard)
	if err == nil {
		err = e.sm.WaitHealthy(ctx, st)
	}
	if err == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	logf("%s: Authelia did not come back (%v) — rolling back", e.node.Name, err)
	if next != nil {
		e.sh(ctx, fmt.Sprintf(`f=%s; if [ -e "$f.prev" ]; then mv "$f.prev" "$f"; else rm -f "$f"; fi`, f))
	}
	if undo != nil {
		undo()
	}
	if rerr := e.sm.Restart(ctx, st, []string{e.c.Service}, io.Discard); rerr == nil {
		rerr = e.sm.WaitHealthy(ctx, st)
		if rerr != nil {
			return fmt.Errorf("%v; after rolling back Authelia is still unhealthy: %v", err, rerr)
		}
	}
	return fmt.Errorf("%v — rolled back to the previous clients", err)
}

// validationErrors keeps what `authelia validate-config` says up to its
// "Error:" line; the command's usage text follows it.
func validationErrors(out string) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		keep = append(keep, l)
		if strings.HasPrefix(l, "Error:") {
			break
		}
	}
	return strings.Join(keep, "\n")
}

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

func (e *authEnv) clientCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(authUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("auth client "+sub, flag.ExitOnError)
	var redirects multi
	fs.Var(&redirects, "redirect", "redirect URI (repeat for several; on set, replaces the list)")
	name := fs.String("name", "", "display name on the consent screen")
	policy := fs.String("policy", "", "authorization policy: one_factor (default), two_factor or a custom policy")
	scopes := fs.String("scopes", "", "comma-separated scopes (default openid,profile,email)")
	pkce := fs.Bool("pkce", false, "require PKCE (S256)")
	public := fs.Bool("public", false, "public client without a secret (native/SPA); implies --pkce")
	consent := fs.String("consent", "", "consent mode: pre-configured (default, remembered 1 year), explicit or implicit")
	method := fs.String("auth-method", "", "token endpoint auth: client_secret_basic (default) or client_secret_post")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	fs.Parse(reorderFlags(args[1:], "redirect", "name", "policy", "scopes", "consent", "auth-method"))
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	if sub == "list" {
		return e.listClients(ctx)
	}
	if fs.NArg() != 1 {
		return errors.New(authUsage)
	}
	id := fs.Arg(0)
	if err := auth.ValidName(id); err != nil {
		return err
	}
	c, err := e.loadClients(ctx)
	if err != nil {
		return err
	}
	cur, exists := c.Get(id)
	if sub != "add" && !exists {
		return fmt.Errorf("no client %q (see `home auth client list`)", id)
	}
	digest, secretFile := id+".digest", id+".secret"

	switch sub {
	case "show":
		e.printClient(cur)
		return nil

	case "add":
		x := auth.Client{ID: id, Name: *name, Policy: *policy, Redirects: redirects, Scopes: splitList(*scopes),
			PKCE: *pkce, Public: *public, Consent: *consent, AuthMethod: *method}
		if err := c.Add(x, e.c.Mount); err != nil {
			return err
		}
		if _, err := e.configFiles(ctx); err != nil {
			return err
		}
		if err := confirm(*yes, fmt.Sprintf("Add client %s? %s", id, e.restartNote())); err != nil {
			return err
		}
		secret := ""
		var undo func() error
		if !x.Public {
			var d string
			if secret, d, err = e.newSecret(ctx, "pbkdf2"); err != nil {
				return err
			}
			if err := e.writeFile(ctx, digest, d); err != nil {
				return err
			}
			undo = func() error { return e.sh(ctx, "rm -f "+remote.Quote(e.path(digest))) }
		}
		if err := e.applyClients(ctx, c, undo); err != nil {
			return err
		}
		x, _ = c.Get(id)
		fmt.Printf("✓ client %s added\n", id)
		if secret != "" {
			fmt.Printf("  client secret  %s\n  (shown once — put it in the app now; `home auth client rotate %s` makes a new one)\n", secret, id)
		}
		e.printClient(x)
		return nil

	case "set":
		var ch auth.ClientChange
		if given["redirect"] {
			r := []string(redirects)
			ch.Redirects = &r
		}
		if given["name"] {
			ch.Name = name
		}
		if given["policy"] {
			ch.Policy = policy
		}
		if given["scopes"] {
			s := splitList(*scopes)
			ch.Scopes = &s
		}
		if given["pkce"] {
			ch.PKCE = pkce
		}
		if given["consent"] {
			ch.Consent = consent
		}
		if given["auth-method"] {
			ch.AuthMethod = method
		}
		if given["public"] {
			return errors.New("a client cannot switch between public and confidential; add a new one")
		}
		if ch == (auth.ClientChange{}) {
			return errors.New("nothing to change (see `home auth` for the flags)")
		}
		if err := c.Set(id, ch); err != nil {
			return err
		}
		if err := confirm(*yes, fmt.Sprintf("Update client %s? %s", id, e.restartNote())); err != nil {
			return err
		}
		if err := e.applyClients(ctx, c, nil); err != nil {
			return err
		}
		x, _ := c.Get(id)
		fmt.Printf("✓ client %s updated\n", id)
		e.printClient(x)
		return nil

	case "rotate":
		if cur.Public {
			return fmt.Errorf("%s is a public client: it has no secret", id)
		}
		if err := confirm(*yes, fmt.Sprintf("Rotate the secret of %s? The app fails to sign anyone in until it has the new one. %s", id, e.restartNote())); err != nil {
			return err
		}
		secret, d, err := e.newSecret(ctx, "pbkdf2")
		if err != nil {
			return err
		}
		p := remote.Quote(e.path(digest))
		if err := e.sh(ctx, fmt.Sprintf(`if [ -e %[1]s ]; then cp -p %[1]s %[1]s.prev; fi`, p)); err != nil {
			return err
		}
		if err := e.writeFile(ctx, digest, d); err != nil {
			return err
		}
		undo := func() error { return e.sh(ctx, fmt.Sprintf(`if [ -e %[1]s.prev ]; then mv %[1]s.prev %[1]s; fi`, p)) }
		if err := e.applyClients(ctx, nil, undo); err != nil {
			return err
		}
		e.sh(ctx, fmt.Sprintf("rm -f %s.prev %s", p, remote.Quote(e.path(secretFile))))
		fmt.Printf("✓ new secret for %s: %s\n  (shown once — put it in the app now)\n", id, secret)
		return nil

	case "rm", "remove", "delete":
		if err := c.Remove(id); err != nil {
			return err
		}
		if err := confirm(*yes, fmt.Sprintf("Delete client %s? Its users can no longer sign in to it through Authelia. %s", id, e.restartNote())); err != nil {
			return err
		}
		if err := e.applyClients(ctx, c, nil); err != nil {
			return err
		}
		e.sh(ctx, fmt.Sprintf("rm -f %s %s %s.prev", remote.Quote(e.path(digest)), remote.Quote(e.path(secretFile)), remote.Quote(e.path(digest))))
		fmt.Printf("✓ client %s deleted\n", id)
		return nil
	}
	return errors.New(authUsage)
}

func (e *authEnv) listClients(ctx context.Context) error {
	c, err := e.loadClients(ctx)
	if err != nil {
		return err
	}
	cs := c.List()
	if len(cs) == 0 {
		fmt.Println("No clients in clients.yml — add one with `home auth client add`.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CLIENT\tNAME\tPOLICY\tTYPE\tCONSENT\tREDIRECTS")
	for _, x := range cs {
		typ := "confidential"
		if x.Public {
			typ = "public"
		}
		if x.PKCE {
			typ += "+pkce"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", x.ID, x.Name, x.Policy, typ, x.Consent, strings.Join(x.Redirects, " "))
	}
	return w.Flush()
}

func (e *authEnv) printClient(x auth.Client) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  client id\t%s\n", x.ID)
	fmt.Fprintf(w, "  name\t%s\n", x.Name)
	if e.c.URL != "" {
		fmt.Fprintf(w, "  issuer\t%s\n", e.issuer())
		fmt.Fprintf(w, "  discovery\t%s/.well-known/openid-configuration\n", e.issuer())
	}
	typ := "confidential (" + x.AuthMethod + ")"
	if x.Public {
		typ = "public (no secret)"
	}
	fmt.Fprintf(w, "  type\t%s\n", typ)
	fmt.Fprintf(w, "  pkce\t%v\n", x.PKCE)
	fmt.Fprintf(w, "  policy\t%s\n", x.Policy)
	fmt.Fprintf(w, "  consent\t%s\n", x.Consent)
	fmt.Fprintf(w, "  scopes\t%s\n", strings.Join(x.Scopes, " "))
	rs := append([]string(nil), x.Redirects...)
	sort.Strings(rs)
	for i, r := range rs {
		label := ""
		if i == 0 {
			label = "redirects"
		}
		fmt.Fprintf(w, "  %s\t%s\n", label, r)
	}
	w.Flush()
}

// ── one-time codes ──────────────────────────────────────────────────────────

// code prints what Authelia's filesystem notifier last wrote; with --wait it
// waits up to 5 minutes for a newer message.
func (e *authEnv) code(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("auth code", flag.ExitOnError)
	wait := fs.Bool("wait", false, "wait for the next notification")
	fs.Parse(args)
	file := remote.Quote(e.c.Data + "/notification.txt")
	read := func() (auth.Notification, bool, error) {
		out, err := e.r.Output(ctx, e.node.Name, "cat "+file+" 2>/dev/null || true")
		if err != nil {
			return auth.Notification{}, false, err
		}
		n, ok := auth.ParseNotification(out)
		return n, ok, nil
	}
	n, ok, err := read()
	if err != nil {
		return err
	}
	if *wait {
		last := n.Date
		fmt.Println("Waiting for Authelia's next notification (Ctrl-C to stop)…")
		deadline := time.Now().Add(5 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			if n, ok, err = read(); err != nil {
				return err
			}
			if ok && n.Date.After(last) {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("no new notification within 5 minutes")
			}
		}
	}
	if !ok {
		fmt.Println("No notification yet.")
		return nil
	}
	age := time.Since(n.Date).Round(time.Second)
	fmt.Printf("%s — %s (%s ago)\n", n.Date.Local().Format("2006-01-02 15:04:05"), n.Subject, age)
	if n.Code == "" {
		fmt.Println("(no one-time code in this one)")
		return nil
	}
	fmt.Printf("\n    %s\n\n", n.Code)
	if age > 5*time.Minute {
		fmt.Println("This code is probably expired; request a new one and use `home auth code --wait`.")
	}
	return nil
}
