# Tunnel tokens are not exported by the cloudflare provider v5.
# Retrieve them from: Cloudflare dashboard → Zero Trust → Tunnels → <tunnel> → Configure → token
# Or via CLI: cloudflared tunnel token <tunnel-name>

output "tunnel_id_ab18" {
  description = "Tunnel ID for ab18 — used in cloudflared config.yml and DNS CNAMEs"
  value       = cloudflare_zero_trust_tunnel_cloudflared.ab.id
}

output "tunnel_id_cd" {
  description = "Tunnel ID for cd — used in cloudflared config.yml and DNS CNAMEs"
  value       = cloudflare_zero_trust_tunnel_cloudflared.cd.id
}

output "pages_url_ab18" {
  description = "Cloudflare Pages deployment URL for ab18"
  value       = "https://${cloudflare_pages_project.ab18.name}.pages.dev"
}

output "pages_url_citrusdental" {
  description = "Cloudflare Pages deployment URL for citrusdental-in"
  value       = "https://${cloudflare_pages_project.citrusdental.name}.pages.dev"
}

output "pages_url_citrusdental_admin" {
  description = "Cloudflare Pages deployment URL for admin-citrusdental-in"
  value       = "https://${cloudflare_pages_project.citrusdental_admin.name}.pages.dev"
}

output "tailscale_ip_ab" {
  description = "Tailscale IP of the ab node — use this for split DNS and tailscale up --hostname"
  value       = local.ab_tailscale_ip
}

output "tailscale_ip_cd" {
  description = "Tailscale IP of the cd node"
  value       = local.cd_tailscale_ip
}


output "tailscale_authkey_ab" {
  description = "Auth key to register the ab node — pipe to: sudo tailscale up --authkey <key> --hostname ab"
  value       = tailscale_tailnet_key.ab.key
  sensitive   = true
}

output "tailscale_authkey_cd" {
  description = "Auth key to register the cd node — pipe to: sudo tailscale up --authkey <key> --hostname cd"
  value       = tailscale_tailnet_key.cd.key
  sensitive   = true
}
