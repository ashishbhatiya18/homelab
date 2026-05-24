variable "cloudflare_api_token" {
  description = "Cloudflare API token — needs Zone:Read, DNS:Edit, Cache Rules:Edit, Tunnel:Edit, Pages:Edit"
  type        = string
  sensitive   = true
}

variable "account_id" {
  description = "Cloudflare account ID (same account for all three zones)"
  type        = string
}

# ---------------------------------------------------------------------------
# Zone IDs — find these at Cloudflare dashboard → zone overview → right sidebar
# ---------------------------------------------------------------------------

variable "zone_id_ab18" {
  description = "Zone ID for ab18.in"
  type        = string
}

variable "zone_id_cd" {
  description = "Zone ID for citrusdental.in"
  type        = string
}

variable "zone_id_nikview" {
  description = "Zone ID for nikview.com"
  type        = string
}

variable "zone_id_presswala" {
  description = "Zone ID for presswala.store"
  type        = string
}

# ---------------------------------------------------------------------------
# Tailscale
# ---------------------------------------------------------------------------

variable "tailscale_oauth_client_id" {
  description = "Tailscale OAuth client ID — admin console → Settings → OAuth clients"
  type        = string
  sensitive   = true
}

variable "tailscale_oauth_client_secret" {
  description = "Tailscale OAuth client secret"
  type        = string
  sensitive   = true
}



