# GitHub Actions — Setup Guide

Two workflows run in this repo:

| Workflow | File | Trigger | What it does |
|---|---|---|---|
| **Validate** | `.github/workflows/validate.yml` | Every push + PR | `docker compose config` on changed files; `shellcheck` on scripts |
| **Terraform** | `.github/workflows/terraform.yml` | Push/PR touching `terraform/**` | `init → fmt → validate → plan` (PR); `+ apply` (merge to main) |

---

## Part A — HCP Terraform Cloud (remote state)

Terraform state is stored in HCP Terraform Cloud (free tier). This is required before any `terraform init` will succeed — locally or in CI.

### Step 1 — Create an HCP Terraform account

1. Go to <https://app.terraform.io> and sign up (free).
2. Create an **organization** (e.g. `your-org-name`).
3. Inside the org, create a workspace:
   - **Name:** `localstack-cloudflare`
   - **Workflow:** CLI-driven (not VCS-driven — GitHub Actions drives it)
   - **Execution mode:** Remote

### Step 2 — Update `terraform/main.tf`

Replace the placeholder org name:

```hcl
cloud {
  organization = "your-org-name"   # ← fill this in
  workspaces {
    name = "localstack-cloudflare"
  }
}
```

Commit and push the change.

### Step 3 — Generate an API token for CI

1. In HCP Terraform: **User Settings → Tokens → Create an API token**
2. Name it `github-actions`
3. Copy the token — you will add it to GitHub secrets in Part B as `TF_CLOUD_TOKEN`

### Step 4 — Local first-run (optional but recommended)

To run terraform locally before CI takes over:

```sh
terraform login           # opens browser, saves token to ~/.terraform.d/credentials.tfrc.json
cd terraform
terraform init
```

---

## Part B — GitHub repository secrets

Go to **GitHub → repo → Settings → Secrets and variables → Actions** and add:

**Secrets** (encrypted, for sensitive values):

| Secret name | Where to get it |
|---|---|
| `CLOUDFLARE_API_TOKEN` | Cloudflare dashboard → My Profile → API Tokens → Create Token (Zone:Read + DNS:Edit + Cache Rules:Edit + Tunnel:Edit + Pages:Edit) |
| `TF_CLOUD_TOKEN` | Token you created in Part A Step 3 |

**Variables** (plaintext, for non-sensitive IDs):

| Variable name | Where to get it |
|---|---|
| `CF_ACCOUNT_ID` | Cloudflare dashboard → any domain → Overview → Account ID (right sidebar) |
| `CF_ZONE_ID_AB18` | Cloudflare dashboard → ab18.in → Overview → Zone ID (right sidebar) |
| `CF_ZONE_ID_CD` | Cloudflare dashboard → citrusdental.in → Overview → Zone ID (right sidebar) |
| `CF_ZONE_ID_NIKVIEW` | Cloudflare dashboard → nikview.com → Overview → Zone ID (right sidebar) |
| `CF_ZONE_ID_PRESSWALA` | Cloudflare dashboard → presswala.store → Overview → Zone ID (right sidebar) |

These map to Terraform variables via the workflow's `env:` block:

```
CLOUDFLARE_API_TOKEN → TF_VAR_cloudflare_api_token  (secret)
TF_CLOUD_TOKEN       → TF_TOKEN_app_terraform_io     (secret, consumed by terraform CLI)
CF_ACCOUNT_ID        → TF_VAR_account_id             (variable)
CF_ZONE_ID_AB18      → TF_VAR_zone_id_ab18           (variable)
CF_ZONE_ID_CD        → TF_VAR_zone_id_cd             (variable)
CF_ZONE_ID_NIKVIEW   → TF_VAR_zone_id_nikview        (variable)
CF_ZONE_ID_PRESSWALA → TF_VAR_zone_id_presswala      (variable)
```

---

## Part C — First-run: import existing Cloudflare resources

If Cloudflare resources (DNS records, cache rules, tunnel) already exist, Terraform must
import them before it can manage them. Run this **once, locally**, after `terraform init`.

```sh
cd terraform
export TF_VAR_cloudflare_api_token="your-api-token"
export TF_VAR_account_id="your-account-id"
export TF_VAR_zone_id_ab18="your-ab18-zone-id"
export TF_VAR_zone_id_cd="your-citrusdental-zone-id"
export TF_VAR_zone_id_nikview="your-nikview-zone-id"
export TF_VAR_zone_id_presswala="your-presswala-zone-id"
```

**Step 1 — look up DNS record IDs for ab18.in** (the import command requires both the address and the ID):

```sh
for name in auth dock draw hub pad pdf photos vault whoami; do
  id=$(curl -s -H "Authorization: Bearer $TF_VAR_cloudflare_api_token" \
    "https://api.cloudflare.com/client/v4/zones/$TF_VAR_zone_id_ab18/dns_records?name=${name}.ab18.in" \
    | jq -r '.result[0].id')
  echo "$name → $id"
done
```

**Step 2 — import DNS records** (substitute the actual IDs from above):

```sh
for name in auth dock draw hub pad pdf photos vault whoami; do
  terraform import "cloudflare_dns_record.ab18[\"$name\"]" \
    "$TF_VAR_zone_id_ab18/<record-id-for-$name>"
done
```

**Step 3 — import the tunnel object:**

```sh
# Find your tunnel ID at Cloudflare Zero Trust → Networks → Tunnels → your tunnel → ID
terraform import cloudflare_zero_trust_tunnel_cloudflared.ab \
  "<tunnel-id>"
```

**Step 4 — import cache rulesets** (if they already exist in Cloudflare):

```sh
# ab18.in
curl -s -H "Authorization: Bearer $TF_VAR_cloudflare_api_token" \
  "https://api.cloudflare.com/client/v4/zones/$TF_VAR_zone_id_ab18/rulesets" \
  | jq '.result[] | {id, name}'
terraform import cloudflare_ruleset.ab18_cache "$TF_VAR_zone_id_ab18/<ruleset-id>"
```

**Step 5 — import the Cloudflare Pages project** (if it already exists):

```sh
terraform import cloudflare_pages_project.ab18 "$TF_VAR_account_id/ab18"
```

After all imports succeed, run `terraform plan` — it should show **no changes**.
Push to main to let CI take over all future applies.

---

## Part D — Workflow behaviour reference

### `validate.yml` — runs on every push and PR

- **compose job:** finds compose files changed vs the base commit, runs
  `docker compose -f <file> config --quiet` on each. Fails the PR if any file is invalid.
- **scripts job:** runs `shellcheck scripts/*.sh` on every push.

No secrets needed — this workflow is read-only.

### `terraform.yml` — runs when `terraform/**` changes

**On a pull request:**
1. `terraform init` (connects to HCP Terraform for state)
2. `terraform fmt -check` — fails if any `.tf` file is not formatted
3. `terraform validate`
4. `terraform plan -out=tfplan`
5. Posts a plan summary comment to the PR

**On merge to main:**
1–4 same as above, then:
5. `terraform apply -auto-approve tfplan`

To format files locally before pushing:
```sh
cd terraform && terraform fmt -recursive
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `Error: No valid credential sources found` | `TF_CLOUD_TOKEN` not set | Add secret to repo; verify secret name matches exactly |
| `organization "REPLACE_WITH_TF_ORG" not found` | `main.tf` not updated | Replace org placeholder and push |
| `terraform plan` shows resource already exists | Resource not imported | Run the relevant import command from Part C |
| `fmt -check` fails | `.tf` file not formatted | `cd terraform && terraform fmt -recursive && git commit` |
| compose validate fails on secrets paths | Absolute secret paths (`/home/dietpi/...`) don't exist on the runner | Expected — `config --quiet` skips volume/secret resolution. If you see a syntax error instead, fix it |
| shellcheck SC2086 / SC2046 warnings | Unquoted variables in scripts | Quote the variable or add `# shellcheck disable=SC2086` with a comment |
