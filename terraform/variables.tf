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

variable "tailscale_hostname_ab" {
  description = "Machine name of the ab node in the tailnet (set during `tailscale up --hostname`)"
  type        = string
  default     = "dietpi-l"
}

variable "tailscale_hostname_cd" {
  description = "Machine name of the cd node in the tailnet"
  type        = string
  default     = "dietpi"
}

variable "tailscale_nodes_registered" {
  description = "Set to true after both nodes are registered in the tailnet to enable split DNS resources"
  type        = bool
  default     = false
}

