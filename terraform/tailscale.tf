# ---------------------------------------------------------------------------
# Tailscale - DNS + node auth keys
# ---------------------------------------------------------------------------

resource "tailscale_dns_preferences" "main" {
  magic_dns = true
}

# Split DNS — forward these domains to dnsdist on the cd node (host network, Tailscale IP)
resource "tailscale_dns_split_nameservers" "ab18" {
  domain      = "ab18.in"
  nameservers = ["100.66.183.5"]
}

resource "tailscale_dns_split_nameservers" "citrusdental" {
  domain      = "citrusdental.in"
  nameservers = ["100.66.183.5"]
}


# Reusable pre-authorised keys - nodes register without manual approval.
# Keys rotate every 90 days; update secrets.auto.tfvars and re-register if expired.
resource "tailscale_tailnet_key" "ab" {
  reusable      = true
  ephemeral     = false
  preauthorized = true
  description   = "ab node - native Tailscale"
  expiry        = 7776000 # 90 days in seconds
  tags          = ["tag:homelab"]
}

resource "tailscale_tailnet_key" "cd" {
  reusable      = true
  ephemeral     = false
  preauthorized = true
  description   = "cd node - native Tailscale"
  expiry        = 7776000
  tags          = ["tag:homelab"]
}
