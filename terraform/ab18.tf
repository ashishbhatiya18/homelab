# ---------------------------------------------------------------------------
# ab18.in
# Node: ab — amd64 server running Traefik + Cloudflare tunnel
# All tunnel traffic enters via Cloudflare and is routed to Traefik internally.
# ---------------------------------------------------------------------------

locals {
  ab18_hostnames = toset([
    "auth",
    "code",
    "draw",
    "pad",
    "pdf",
    "photos",
    "stack",
    "vault",
    "whoami",
  ])
}

# ── Tunnel ──────────────────────────────────────────────────────────────────
# Import existing tunnel with:
#   terraform import cloudflare_zero_trust_tunnel_cloudflared.ab <tunnel-id>

resource "cloudflare_zero_trust_tunnel_cloudflared" "ab" {
  account_id = var.account_id
  name       = "ab18-localstack"
  config_src = "cloudflare"

  lifecycle {
    ignore_changes = [config_src]
  }
}

# Tunnel ingress — all subdomain traffic → Traefik inside the node
resource "cloudflare_zero_trust_tunnel_cloudflared_config" "ab" {
  account_id = var.account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.ab.id

  config = {
    origin_request = {
      http2_origin  = true
      no_tls_verify = false
    }

    ingress = concat(
      [for hostname in sort(tolist(local.ab18_hostnames)) : {
        hostname = "${hostname}.ab18.in"
        service  = "https://traefik:443"
        origin_request = {
          origin_server_name = "${hostname}.ab18.in"
          http_host_header   = "${hostname}.ab18.in"
        }
      }],
      [{ service = "http_status:404" }]
    )
  }
}

# ── DNS — subdomain CNAMEs → tunnel ─────────────────────────────────────────

resource "cloudflare_dns_record" "ab18" {
  for_each = local.ab18_hostnames

  zone_id = var.zone_id_ab18
  name    = each.key
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.ab.id}.cfargotunnel.com"
  proxied = true
  ttl     = 1
}

# ── Cloudflare Pages — ab18-website (Next.js SSG) ───────────────────────────
# Import existing project with:
#   terraform import cloudflare_pages_project.ab18 8a420fa9dc8c2fc86b4caa6c8673ea94/ab18

resource "cloudflare_pages_project" "ab18" {
  account_id        = var.account_id
  name              = "ab18"
  production_branch = "main"

  build_config = {
    build_command   = "yarn install && yarn build"
    destination_dir = "out"
    root_dir        = ""
  }

  source = {
    type = "github"
    config = {
      owner                          = "ashishbhatiya18"
      repo_name                      = "ab18-website"
      production_branch              = "main"
      pr_comments_enabled            = true
      preview_deployment_setting     = "all"
      preview_branch_includes        = ["*"]
      production_deployments_enabled = true
    }
  }

  deployment_configs = {
    production = {
      env_vars = {
        NEXT_PUBLIC_IMMICH_SHARE_URL = { value = var.ab18_immich_share_url, type = "plain_text" }
        NEXT_PUBLIC_GA_ID            = { value = var.ab18_ga_id, type = "plain_text" }
        NEXT_PUBLIC_RESUME_URL       = { value = var.ab18_resume_url, type = "plain_text" }
      }
      compatibility_date  = "2024-01-01"
      compatibility_flags = []
    }
    preview = {
      env_vars = {
        NEXT_PUBLIC_IMMICH_SHARE_URL = { value = var.ab18_immich_share_url, type = "plain_text" }
        NEXT_PUBLIC_GA_ID            = { value = var.ab18_ga_id, type = "plain_text" }
        NEXT_PUBLIC_RESUME_URL       = { value = var.ab18_resume_url, type = "plain_text" }
      }
      compatibility_date  = "2024-01-01"
      compatibility_flags = []
    }
  }
}

# Custom domain — root domain ab18.in → Pages project
resource "cloudflare_pages_domain" "ab18_root" {
  account_id   = var.account_id
  project_name = cloudflare_pages_project.ab18.name
  name         = "ab18.in"
}

# DNS — root domain CNAME → Pages (Cloudflare flattens CNAME at apex)
resource "cloudflare_dns_record" "ab18_root" {
  zone_id = var.zone_id_ab18
  name    = "ab18.in"
  type    = "CNAME"
  content = "${cloudflare_pages_project.ab18.name}.pages.dev"
  proxied = true
  ttl     = 1
}

# ── Cache rules ──────────────────────────────────────────────────────────────

resource "cloudflare_ruleset" "ab18_cache" {
  zone_id = var.zone_id_ab18
  name    = "ab18.in cache rules"
  kind    = "zone"
  phase   = "http_request_cache_settings"

  rules = [
    {
      description = "Bypass cache for Vaultwarden — prevents Set-Cookie replay across clients"
      expression  = "(http.host eq \"vault.ab18.in\")"
      action      = "set_cache_settings"
      action_parameters = {
        cache = false
      }
    }
  ]
}
