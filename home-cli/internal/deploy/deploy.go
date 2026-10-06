// Package deploy rolls node bundles out to nodes over SSH — no agent and no
// git checkout on the node.
//
// A bundle is a FROM-scratch image (built by CI) holding /stacks: the node's
// stacks plus node.sh and node.conf. On the node, with stacks_dir =
// <base>/stacks:
//
//	<base>/releases/<digest>/stacks/  each bundle, extracted (the newest few are kept)
//	<base>/stacks/                    the deployed files compose runs from
//	<base>/stacks/.release            what is deployed: revision, digest, previous
//
// Deploying syncs the new release into stacks_dir (changed files are
// rewritten in place, so single-file bind mounts see them; files the release
// dropped are deleted; files that never came from a release are left alone),
// then starts every changed stack with node.sh in the node's start order,
// waiting for each to be healthy. If one is not, the previous release is
// synced back and the stacks started so far are started again from it.
package deploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/registry"
	"github.com/ashishbhatiya18/home/internal/remote"
	"github.com/ashishbhatiya18/home/internal/stack"
)

// Keep is how many extracted releases stay on a node (the deployed and the
// previous one are always kept).
const Keep = 5

type Deployer struct {
	R     *remote.Runner
	SM    *stack.Manager
	Creds func(host string) *registry.Cred
	Logf  func(string, ...any)
	// Hook returns the pre-update hook for a stack ("node/stack"), or nil. It
	// runs before that stack is started from the new release.
	Hook func(id string) func() error
	// Out receives node.sh/compose output.
	Out io.Writer
}

// Release is what a node has deployed (from its .release file).
type Release struct {
	Revision, Digest, Image, Previous, DeployedAt string
}

// Short is the revision's first 12 characters, or "none".
func (r Release) Short() string {
	if r.Revision == "" {
		return "none"
	}
	if len(r.Revision) > 12 {
		return r.Revision[:12]
	}
	return r.Revision
}

type paths struct{ live, releases string }

func pathsOf(n homecfg.Node) paths {
	live := strings.TrimRight(n.StacksDir, "/")
	return paths{live: live, releases: path.Join(path.Dir(live), "releases")}
}

func (p paths) releaseDir(digest string) string {
	return p.releases + "/" + strings.TrimPrefix(digest, "sha256:")
}

// content is where a release's stacks are, inside its release dir.
func (p paths) content(digest string) string { return p.releaseDir(digest) + "/stacks" }

func repoOf(bundle string) string {
	if i := strings.LastIndex(bundle, ":"); i > strings.LastIndex(bundle, "/") {
		return bundle[:i]
	}
	return bundle
}

// Current reads the node's .release (zero Release if nothing is deployed).
func (d *Deployer) Current(ctx context.Context, n homecfg.Node) (Release, error) {
	out, err := d.R.Output(ctx, n.Name, "cat "+remote.Quote(pathsOf(n).live+"/.release")+" 2>/dev/null || true")
	if err != nil {
		return Release{}, err
	}
	return parseRelease(out), nil
}

func parseRelease(s string) Release {
	var r Release
	for _, l := range strings.Split(s, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		switch k {
		case "revision":
			r.Revision = v
		case "digest":
			r.Digest = v
		case "image":
			r.Image = v
		case "previous":
			r.Previous = v
		case "deployed_at":
			r.DeployedAt = v
		}
	}
	return r
}

// Latest asks the registry for the digest of the node's bundle at tag.
func (d *Deployer) Latest(ctx context.Context, n homecfg.Node, tag string) (string, error) {
	if n.Bundle == "" {
		return "", fmt.Errorf("%s has no bundle configured", n.Name)
	}
	ref, err := registry.Parse(repoOf(n.Bundle) + ":" + tag)
	if err != nil {
		return "", err
	}
	var cred *registry.Cred
	if d.Creds != nil {
		cred = d.Creds(ref.Registry)
	}
	return registry.Digest(ctx, ref, cred)
}

// Plan is what deploying one release to one node would change.
type Plan struct {
	Node    homecfg.Node
	From    Release  // deployed now (zero on a first deploy)
	To      Release  // Revision, Digest, Image of the new release
	Changed []string // stacks to start, in the node's start order
	Removed []string // stacks gone from the release; left running
	Files   []string // node-level files that change (node.sh, node.conf)
	// Recreate starts every stack with fresh containers (node.sh recreate),
	// not only the changed ones.
	Recreate bool
}

func (p *Plan) Empty() bool { return len(p.Changed)+len(p.Removed)+len(p.Files) == 0 }

const prepareScript = `set -euo pipefail
ref=%[1]s rel=%[2]s dir=%[3]s live=%[4]s
mkdir -p "$rel"
if [ ! -d "$dir" ]; then
  docker pull -q "$ref" >/dev/null
  tmp="$dir.tmp"; rm -rf "$tmp"; mkdir -p "$tmp"
  cid=$(docker create "$ref" none)
  trap 'docker rm -f "$cid" >/dev/null 2>&1 || true' EXIT
  docker export "$cid" | tar -x -C "$tmp" stacks
  docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$ref" > "$tmp/REVISION"
  mv "$tmp" "$dir"
fi
new="$dir/stacks"
[ -d "$new" ] || { echo "the bundle has no /stacks" >&2; exit 1; }
echo "revision=$(cat "$dir/REVISION")"
for d in "$new"/*/; do
  s=$(basename "$d"); [ -f "$d/compose.yaml" ] || continue
  # Changed: new, or any difference other than files only present on the node.
  if [ ! -d "$live/$s" ] || { diff -rq "$live/$s" "$d" 2>/dev/null || true; } | grep -qv "^Only in $live/$s"; then
    echo "changed=$s"
  fi
done
for d in "$live"/*/; do
  [ -f "$d/compose.yaml" ] || continue
  s=$(basename "$d"); [ -d "$new/$s" ] || echo "removed=$s"
done
for f in node.sh node.conf; do cmp -s "$new/$f" "$live/$f" || echo "file=$f"; done
bash "$new/node.sh" order | sed 's/^/order=/'
`

// Prepare pulls the release (by digest) on the node, extracts it next to the
// others and works out what deploying it changes. Nothing deployed changes.
func (d *Deployer) Prepare(ctx context.Context, n homecfg.Node, digest string, recreate bool) (*Plan, error) {
	from, err := d.Current(ctx, n)
	if err != nil {
		return nil, err
	}
	p := pathsOf(n)
	ref := repoOf(n.Bundle) + "@" + digest
	script := fmt.Sprintf(prepareScript, remote.Quote(ref), remote.Quote(p.releases), remote.Quote(p.releaseDir(digest)), remote.Quote(p.live))
	out, err := d.R.Output(ctx, n.Name, "bash -s <<'HOME_EOF'\n"+script+"HOME_EOF")
	if err != nil {
		return nil, fmt.Errorf("preparing release on %s: %w", n.Name, err)
	}
	plan := &Plan{Node: n, From: from, To: Release{Digest: digest, Image: n.Bundle}, Recreate: recreate}
	changed := map[string]bool{}
	var order []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "revision":
			plan.To.Revision = v
		case "changed":
			changed[v] = true
		case "removed":
			plan.Removed = append(plan.Removed, v)
		case "file":
			plan.Files = append(plan.Files, v)
		case "order":
			order = append(order, v)
		}
	}
	for _, s := range order {
		if changed[s] || recreate {
			plan.Changed = append(plan.Changed, s)
		}
	}
	return plan, nil
}

const syncScript = `set -euo pipefail
new=%[1]s old=%[2]s live=%[3]s
mkdir -p "$live"
rsync -a --checksum --inplace "$new/" "$live/"
if [ -n "$old" ] && [ -d "$old" ]; then
  comm -23 <(cd "$old" && find . -type f | sort) <(cd "$new" && find . -type f | sort) |
    while IFS= read -r f; do rm -f "$live/$f"; echo "removed ${f#./}"; done
fi
`

// sync makes the live dir match release `to`; `from` (may be "") is the
// release being replaced, whose files the new one dropped get deleted.
func (d *Deployer) sync(ctx context.Context, n homecfg.Node, to, from Release) error {
	p := pathsOf(n)
	old := ""
	if from.Digest != "" {
		old = p.content(from.Digest)
	}
	script := fmt.Sprintf(syncScript, remote.Quote(p.content(to.Digest)), remote.Quote(old), remote.Quote(p.live))
	if err := d.R.Run(ctx, n.Name, "bash -s <<'HOME_EOF'\n"+script+"HOME_EOF", nil, d.Out); err != nil {
		return err
	}
	rec := fmt.Sprintf("revision=%s\ndigest=%s\nimage=%s\nprevious=%s\ndeployed_at=%s\n",
		to.Revision, to.Digest, to.Image, to.Previous, time.Now().Format(time.RFC3339))
	f := remote.Quote(p.live + "/.release")
	return d.R.Run(ctx, n.Name, "cat > "+f+".tmp && mv "+f+".tmp "+f, strings.NewReader(rec), nil)
}

func (d *Deployer) start(ctx context.Context, n homecfg.Node, name string, recreate bool) error {
	s := stack.Stack{Node: n.Name, Name: name, Dir: pathsOf(n).live}
	run := d.SM.Start
	if recreate {
		run = d.SM.Recreate
	}
	if err := run(ctx, s, d.Out); err != nil {
		return err
	}
	return d.SM.WaitHealthy(ctx, s)
}

// Apply deploys a prepared plan. On a stack that does not come up healthy it
// rolls the node back to the previous release and returns the error.
func (d *Deployer) Apply(ctx context.Context, p *Plan) error {
	n := p.Node
	to := p.To
	to.Previous = p.From.Digest
	d.Logf("%s: deploying %s (was %s)", n.Name, to.Short(), p.From.Short())
	if err := d.sync(ctx, n, to, p.From); err != nil {
		return fmt.Errorf("syncing release: %w", err)
	}
	var started []string
	for _, s := range p.Changed {
		id := n.Name + "/" + s
		if d.Hook != nil {
			if h := d.Hook(id); h != nil {
				if err := h(); err != nil {
					return d.rollback(ctx, p, started, fmt.Errorf("%s: pre-update hook: %w", id, err))
				}
			}
		}
		d.Logf("%s: starting %s", n.Name, s)
		started = append(started, s)
		if err := d.start(ctx, n, s, p.Recreate); err != nil {
			return d.rollback(ctx, p, started, fmt.Errorf("%s: %w", id, err))
		}
	}
	for _, s := range p.Removed {
		d.Logf("%s: stack %s is no longer in the release; its containers keep running (node.sh down %s removes them)", n.Name, s, s)
	}
	d.prune(ctx, n, to)
	d.Logf("%s: %s deployed ✓", n.Name, to.Short())
	return nil
}

func (d *Deployer) rollback(ctx context.Context, p *Plan, started []string, cause error) error {
	n := p.Node
	if p.From.Digest == "" {
		return fmt.Errorf("%w — first deploy on %s, nothing to roll back to", cause, n.Name)
	}
	d.Logf("%s: %v; rolling back to %s", n.Name, cause, p.From.Short())
	if _, err := d.Prepare(ctx, n, p.From.Digest, false); err != nil { // re-extracts it if it was pruned
		return fmt.Errorf("%w — AND rollback failed: %v", cause, err)
	}
	to := p.To
	to.Previous = p.From.Digest
	if err := d.sync(ctx, n, p.From, to); err != nil {
		return fmt.Errorf("%w — AND rollback failed: %v", cause, err)
	}
	var errs []string
	for _, s := range started {
		if err := d.start(ctx, n, s, false); err != nil {
			errs = append(errs, s+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w — rolled back to %s, but not healthy: %s", cause, p.From.Short(), strings.Join(errs, "; "))
	}
	return fmt.Errorf("%w — rolled back to %s", cause, p.From.Short())
}

// prune keeps the newest Keep releases (always the deployed and previous
// one) and drops dangling bundle images.
func (d *Deployer) prune(ctx context.Context, n homecfg.Node, cur Release) {
	p := pathsOf(n)
	keep := []string{strings.TrimPrefix(cur.Digest, "sha256:"), strings.TrimPrefix(cur.Previous, "sha256:")}
	script := fmt.Sprintf(`cd %s 2>/dev/null || exit 0
ls -1dt -- */ 2>/dev/null | tail -n +%d | while IFS= read -r r; do
  r=${r%%/}; [ "$r" = %s ] || [ "$r" = %s ] || rm -rf -- "$r"
done
docker image prune -f --filter label=homelab.bundle=true >/dev/null 2>&1 || true`,
		remote.Quote(p.releases), Keep+1, remote.Quote(keep[0]), remote.Quote(keep[1]))
	if err := d.R.Run(ctx, n.Name, script, nil, nil); err != nil {
		d.Logf("%s: pruning old releases: %v", n.Name, err)
	}
}
