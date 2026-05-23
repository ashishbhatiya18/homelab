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
    "apex-spf"    = { name = "@", content = "v=spf1 include:_spf.mx.cloudflare.net ~all" }
    "apex-dmarc"  = { name = "_dmarc", content = "v=DMARC1; p=none; rua=mailto:f79684f9bff14c83a2843656e907596b@dmarc-reports.cloudflare.net" }
    "dkim-cf2024" = { name = "cf2024-1._domainkey", content = "v=DKIM1; h=sha256; k=rsa; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAiweykoi+o48IOGuP7GR3X0MOExCUDY/BCRHoWBnh3rChl7WhdyCxW3jgq1daEjPPqoi7sJvdg5hEQVsgVRQP4DcnQDVjGMbASQtrY4WmB1VebF+RPJB2ECPsEDTpeiI5ZyUAwJaVX7r6bznU67g7LvFq35yIo4sdlmtZGV+i0H4cpYH9+3JJ78km4KXwaf9xUJCWF6nxeD+qG6Fyruw1Qlbds2r85U9dkNDVAS3gioCvELryh1TxKGiVTkg4wqHTyHfWsp7KD3WQHYJn0RyfJJu6YEmL77zonn7p2SRMvTMP3ZEXibnC9gz3nnhR6wcYL8Q7zXypKTMD58bTixDSJwIDAQAB" }
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
