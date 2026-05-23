# ---------------------------------------------------------------------------
# citrusdental.in
# Node: cd — separate Raspberry Pi with its own Cloudflare tunnel + Traefik
# ---------------------------------------------------------------------------

locals {
  cd_tunnel_hostnames = toset([
    "api"
  ])

  cd_cname_records = {
    "admin"         = { name = "admin", content = "citrus-dental-admin.pages.dev", proxied = true }
    "apex"          = { name = "@", content = "citrus-dental-website.pages.dev", proxied = true }
    "www"           = { name = "www", content = "citrus-dental-website.pages.dev", proxied = true }
    "dmarc-mail"    = { name = "_dmarc.mail", content = "_dmarc.mail.citrusdental.in.hosted-dmarc.mailer91.com", proxied = false }
    "mailer91-mail" = { name = "mailer91.mail", content = "email.mailer91.com", proxied = false }
  }

  cd_mx_records = {
    "apex-route1" = { name = "@", content = "route1.mx.cloudflare.net", priority = 78 }
    "apex-route2" = { name = "@", content = "route2.mx.cloudflare.net", priority = 14 }
    "apex-route3" = { name = "@", content = "route3.mx.cloudflare.net", priority = 97 }
    "mail-mx1"    = { name = "mail", content = "mx1.mailer91.com", priority = 5 }
    "mail-mx2"    = { name = "mail", content = "mx2.mailer91.com", priority = 10 }
  }

  cd_txt_records = {
    "apex-spf"      = { name = "@", content = "v=spf1 include:_spf.mx.cloudflare.net ~all" }
    "apex-google-1" = { name = "@", content = "google-site-verification=R_yZ9MXc0dPizBvOwQBKe0k3mk_i3BjHaahaFVWDifU" }
    "apex-google-2" = { name = "@", content = "google-site-verification=1ZMX7S28-ltxtskExd9wxMtWSxnpvO2zGOrmsgqaDFc" }
    "mail-spf"      = { name = "mail", content = "v=spf1 include:mailer91.com ~all" }
    # dkim-cf2024 omitted — managed by Cloudflare Email Routing, cannot be modified via API
    "dkim-spaceship" = { name = "spaceship._domainkey.mail", content = "v=DKIM1;k=rsa;p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCW2UxfSdaD6l2YHlbGetsh0naUlhBYNrPFziLUKNsw1VTtk/UOM5j9zDQpEHbn9jTGiNooKjVU/i4uWBNEi3jn4409W/yeQeqF4/ye/ZYUCuZscGn+9OBHuDye9FwOz7SVnAnJpA0WxWOFZrrk2zepaweOytLSH1FKGkx2CsbvTwIDAQAB" }
  }
}

# Tunnel object
resource "cloudflare_zero_trust_tunnel_cloudflared" "cd" {
  account_id = var.account_id
  name       = "citrusdental.in"
  config_src = "cloudflare"
}

# Tunnel ingress config — managed here so routing rules are in git
resource "cloudflare_zero_trust_tunnel_cloudflared_config" "cd" {
  account_id = var.account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.cd.id

  config = {
    ingress = [
      {
        hostname = "*.citrusdental.in"
        service  = "https://traefik:443"
        origin_request = {
          no_tls_verify = true
        }
      },
      {
        service = "http_status:404"
      }
    ]
  }
}

# DNS — tunnel CNAMEs
resource "cloudflare_dns_record" "cd" {
  for_each = local.cd_tunnel_hostnames

  zone_id = var.zone_id_cd
  name    = each.key
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.cd.id}.cfargotunnel.com"
  proxied = true
  ttl     = 1
}

# DNS — pages + mail CNAMEs
resource "cloudflare_dns_record" "cd_cname" {
  for_each = local.cd_cname_records

  zone_id = var.zone_id_cd
  name    = each.value.name
  type    = "CNAME"
  content = each.value.content
  proxied = each.value.proxied
  ttl     = 1
}

# DNS — MX records
resource "cloudflare_dns_record" "cd_mx" {
  for_each = local.cd_mx_records

  zone_id  = var.zone_id_cd
  name     = each.value.name
  type     = "MX"
  content  = each.value.content
  priority = each.value.priority
  proxied  = false
  ttl      = 1
}

# DNS — TXT records
resource "cloudflare_dns_record" "cd_txt" {
  for_each = local.cd_txt_records

  zone_id = var.zone_id_cd
  name    = each.value.name
  type    = "TXT"
  content = each.value.content
  proxied = false
  ttl     = 1
}

# Pages — direct upload deployments
resource "cloudflare_pages_project" "citrusdental" {
  account_id        = var.account_id
  name              = "citrusdental-in"
  production_branch = "rewrite"
}

resource "cloudflare_pages_project" "citrusdental_admin" {
  account_id        = var.account_id
  name              = "admin-citrusdental-in"
  production_branch = "rewrite"
}

# Cache rules
resource "cloudflare_ruleset" "cd_cache" {
  zone_id = var.zone_id_cd
  name    = "citrusdental.in cache rules"
  kind    = "zone"
  phase   = "http_request_cache_settings"

  rules = [
    {
      description = "Bypass cache for API — dynamic responses should never be served stale"
      expression  = "(http.host eq \"api.citrusdental.in\")"
      action      = "set_cache_settings"
      action_parameters = {
        cache = false
      }
    }
  ]
}
