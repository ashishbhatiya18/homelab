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

variable "tailscale_nodes_registered" {
  description = "Set to true once nodes have been registered — used to gate auth key creation"
  type        = bool
  default     = false
}

# ---------------------------------------------------------------------------
# ab18-website — Cloudflare Pages env vars
# ---------------------------------------------------------------------------

variable "ab18_immich_share_url" {
  description = "Immich public album share URL for the photography page (NEXT_PUBLIC_IMMICH_SHARE_URL)"
  type        = string
  # Not marked sensitive: NEXT_PUBLIC_ vars are baked into the client bundle at build time
  # and are visible in page source. Marking sensitive here propagates into deployment_configs,
  # which triggers a provider v5 bug ("inconsistent values for sensitive attribute") because
  # the CF API returns the value without the sensitive marker, causing a state mismatch.
}

variable "ab18_ga_id" {
  description = "Google Analytics 4 measurement ID (NEXT_PUBLIC_GA_ID)"
  type        = string
}

variable "ab18_resume_url" {
  description = "Google Docs PDF export URL for the résumé (NEXT_PUBLIC_RESUME_URL)"
  type        = string
}



