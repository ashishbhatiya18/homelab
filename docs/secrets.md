# Secrets

Secrets never live in git. Each node keeps them under
`/home/dietpi/localstack/secrets/` (next to `stacks/` and `data/`), and stacks
reference them by absolute path from their `compose.yaml`.

## Layout: two kinds of secret

| Kind | Path | Holds | Rotated by |
|---|---|---|---|
| **Secret file** (default) | `secrets/<stack>/<name>` | one value, or one document (PEM, JSON, YAML) | replacing the whole file |
| **Env file** (exception) | `secrets/<stack>.env` | `KEY='value'` lines, secrets only | changing one line |

- **Prefer secret files.** Mount them with compose `secrets:` (or a read-only
  bind mount) and point the app at the path, usually with a `*_FILE` variable.
  A secret file doesn't show up in `docker inspect` or `docker compose config`;
  environment variables do.
- **Use the stack's env file only when the app can't read a file** (gatus,
  watchtower's notification URL, apps that don't support `*_FILE` yet). Write
  values single-quoted: compose expands `$VAR` and cuts ` #…` in unquoted values.
- **Plain configuration is not a secret.** Time zones, URLs, CORS lists and
  feature flags go in the stack's compose `environment:`, in git.
- Files an app writes itself (OAuth tokens it refreshes, Syncthing's identity,
  Authelia's `users.yml`/`clients.yml`, managed with `home auth`) are secret
  files too, but nothing else overwrites them.
- One stack, one directory: a value two stacks need (a shared API key) is kept
  in each stack's own file.
- Owner and mode: readable by the user the container runs as (usually dietpi,
  1000:1000), mode 600; directories 700.

## Moving a stack to this layout

Each stack moves on its own, with no downtime:

1. Change its `compose.yaml` to the new paths and add a `secrets.migrate.yaml`
   next to it, saying where each new file comes from (format below). Push.
2. Run `home secret migrate <node>/<stack>`. It reads the mapping from the
   bundle, creates the new files on the node (values are copied node-side and
   never leave it), deploys, and once the stack is healthy and no deployed
   stack references an old file any more, deletes it. A failed deploy rolls
   back to the old compose, which still finds the old files.
3. Remove `secrets.migrate.yaml` in a later commit; it does nothing once
   applied.

Until step 2 runs, `home deploy` (and the background job) refuses the release:
a deploy never starts a stack whose secret files are missing on the node.

`secrets.migrate.yaml` is a list of moves; paths are relative to `secrets/`:

```yaml
- from: cf_dns_api_token                # copy a file (owner and mode kept)
  to: network/cf_dns_api_token
- from: authelia                        # copy a directory
  to: network/authelia
- from: ab-cloudflared.env              # one value from an env file → a secret file
  key: TUNNEL_TOKEN
  to: network/cloudflared_tunnel_token
- from: ab-vaultwarden.env              # some lines of an env file → the new env file
  keys: [ADMIN_TOKEN, SSO_CLIENT_SECRET]
  to: vaultwarden.env
```

A tunnel's token path is also recorded in `cloud/tunnels/<tunnel>.yaml`
(`secret:`); update it in the same commit when a token moves.
