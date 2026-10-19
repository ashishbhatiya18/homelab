# GitHub Actions

| Workflow | Trigger | What it does |
|---|---|---|
| `validate.yml` | every push to `main` and every PR | `docker compose config --quiet` on changed compose files (with stub env files); `shellcheck nodes/node.sh` |
| `node-bundles.yml` | push to `main` touching `nodes/**` or the bundle Dockerfile; by hand | validates **every** compose file of each changed node, then publishes `ghcr.io/ashishbhatiya18/node-<node>` (`:latest`, `:<sha>`) |
| `build-claudecode.yml`, `build-kopia.yml` | push to `main` touching the image's directory | build the custom images `ghcr.io/ashishbhatiya18/{claudecode,kopia}` (nodes never build images) |
| `home-cli.yml` | changes to `home-cli/**`; a `home-cli-vX.Y.Z` tag | vet + tests; on a tag, publish the macOS binaries to GHCR and update `Formula/home.rb` in the Homebrew tap |
| `down-cli.yml` | changes to `down-cli/**`; a `down-cli-vX.Y.Z` tag | tests; on a tag, publish the macOS formula and the Linux release binaries |

Cloudflare (tunnels, routes, DNS) is not deployed by CI: it is declared in
`cloud/` and applied from the Mac with `home cloud plan|apply` (token in the
Keychain, set up with `home cloud setup`).

## `node-bundles.yml`

A bundle is a `FROM scratch` image holding the node's stacks, `node.conf` and
`node.sh` under `/stacks`. Nodes have no checkout and no agent: `home deploy`
(and its background job on the Mac) rolls the bundle out over SSH. Runs are
queued in commit order so an older run never overwrites `:latest`; it can be
run by hand for one node or both.

## Secrets

The release workflows push to the Homebrew tap with `TAP_DEPLOY_KEY` (a write
deploy key scoped to the tap repo); GHCR pushes use the workflow's
`GITHUB_TOKEN`. `validate.yml` and `node-bundles.yml` need no secrets.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| compose validate fails on secrets paths | Absolute secret paths (`/home/dietpi/...`) don't exist on the runner | Expected — stub env files are generated; if you see a syntax error instead, fix it |
| shellcheck SC2086 / SC2046 warnings | Unquoted variables in scripts | Quote the variable or add `# shellcheck disable=SC2086` with a comment |
