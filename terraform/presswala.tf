# ---------------------------------------------------------------------------
# presswala.store
# Cloudflare Email Routing enabled, no tunnel or Pages yet.
# ---------------------------------------------------------------------------

locals {
  presswala_mx_records = {
    "apex-route1" = { content = "route1.mx.cloudflare.net", priority = 95 }
    "apex-route2" = { content = "route2.mx.cloudflare.net", priority = 93 }
    "apex-route3" = { content = "route3.mx.cloudflare.net", priority = 47 }
  }

  presswala_txt_records = {
    "apex-spf"   = { name = "@", content = "v=spf1 include:_spf.mx.cloudflare.net ~all" }
    "apex-dmarc" = { name = "_dmarc", content = "v=DMARC1; p=none; rua=mailto:f79684f9bff14c83a2843656e907596b@dmarc-reports.cloudflare.net" }
    # dkim-cf2024 omitted — managed by Cloudflare Email Routing, cannot be modified via API
  }
}

# DNS — www
resource "cloudflare_dns_record" "presswala_www" {
  zone_id = var.zone_id_presswala
  name    = "www"
  type    = "CNAME"
  content = "presswala.store"
  proxied = true
  ttl     = 1
}

# DNS — MX (Cloudflare Email Routing)
resource "cloudflare_dns_record" "presswala_mx" {
  for_each = local.presswala_mx_records

  zone_id  = var.zone_id_presswala
  name     = "@"
  type     = "MX"
  content  = each.value.content
  priority = each.value.priority
  proxied  = false
  ttl      = 1
}

# DNS — TXT (SPF, DMARC, DKIM)
resource "cloudflare_dns_record" "presswala_txt" {
  for_each = local.presswala_txt_records

  zone_id = var.zone_id_presswala
  name    = each.value.name
  type    = "TXT"
  content = each.value.content
  proxied = false
  ttl     = 1
}
