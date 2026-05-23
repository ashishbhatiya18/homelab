# ---------------------------------------------------------------------------
# nikview.com
# Currently a simple proxied site (no Pages project yet).
# ---------------------------------------------------------------------------

# DNS
resource "cloudflare_dns_record" "nikview_apex" {
  zone_id = var.zone_id_nikview
  name    = "@"
  type    = "A"
  content = "2.57.91.91"
  proxied = true
  ttl     = 1
}

resource "cloudflare_dns_record" "nikview_www" {
  zone_id = var.zone_id_nikview
  name    = "www"
  type    = "CNAME"
  content = "nikview.com"
  proxied = true
  ttl     = 1
}
