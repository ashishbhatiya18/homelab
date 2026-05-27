# ---------------------------------------------------------------------------
# ab18.in
# Node: ab — Raspberry Pi running Traefik + Cloudflare tunnel
# All traffic enters via the tunnel and is routed to Traefik internally.
# ---------------------------------------------------------------------------

locals {
  ab18_hostnames = toset([
    "auth",
    "draw",
    "pad",
    "pdf",
    "photos",
    "vault",
    "whoami",
  ])
}

# Tunnel object — import the existing one with:
#   terraform import cloudflare_zero_trust_tunnel_cloudflared.ab <tunnel-id>
resource "cloudflare_zero_trust_tunnel_cloudflared" "ab" {
  account_id = var.account_id
  name       = "ab18-localstack"
  config_src = "cloudflare"

  lifecycle {
    ignore_changes = [config_src]
  }
}

# Tunnel ingress config — managed here so routing rules are in git
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

# DNS — one proxied CNAME per subdomain pointing at the tunnel
resource "cloudflare_dns_record" "ab18" {
  for_each = local.ab18_hostnames

  zone_id = var.zone_id_ab18
  name    = each.key
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.ab.id}.cfargotunnel.com"
  proxied = true
  ttl     = 1
}

# Pages — static site deployed via Cloudflare Pages
# Import with: terraform import cloudflare_pages_project.ab18 8a420fa9dc8c2fc86b4caa6c8673ea94/ab18
resource "cloudflare_pages_project" "ab18" {
  account_id        = var.account_id
  name              = "ab18"
  production_branch = "main"

  build_config = {
    build_command   = "npx vitepress build"
    destination_dir = ".vitepress/dist"
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
}

# Cache rules
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
