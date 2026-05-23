# ---------------------------------------------------------------------------
# Tailscale - DNS + node auth keys
#
# Two-phase apply:
#   Phase 1 (first time): apply only the auth keys, register the nodes, then
#   run phase 2.
#     terraform apply -target=tailscale_tailnet_key.ab -target=tailscale_tailnet_key.cd
#     sudo tailscale up --authkey $(terraform output -raw tailscale_authkey_ab) --hostname ab  # on ab node
#     sudo tailscale up --authkey $(terraform output -raw tailscale_authkey_cd) --hostname cd  # on cd node
#
#   Phase 2 (after nodes are registered): full apply - picks up device IPs
#   and creates split DNS entries.
#     terraform apply
#
# Per-node DNS server requirement:
#   Split DNS works by routing *.ab18.in queries to the ab node and
#   *.citrusdental.in queries to the cd node. Each node must answer port 53
#   with its own Tailscale IP for that domain.
#
#   On the ab node - add /etc/dnsmasq.d/ab18.conf:
#     address=/ab18.in/<ab_tailscale_ip>   # terraform output tailscale_ip_ab
#
#   On the cd node - pihole handles citrusdental.in, but it's on macvlan.
#   Forward Tailscale DNS to pihole and update pihole hosts entry:
#     iptables -t nat -A PREROUTING -i tailscale0 -p udp --dport 53 \
#       -j DNAT --to-destination 10.10.10.10:53
#     # pihole hosts: replace 10.10.10.12 with <cd_tailscale_ip> for citrusdental.in
# ---------------------------------------------------------------------------

data "tailscale_device" "ab" {
  count    = var.tailscale_nodes_registered ? 1 : 0
  hostname = var.tailscale_hostname_ab
  wait_for = "60s"
}

data "tailscale_device" "cd" {
  count    = var.tailscale_nodes_registered ? 1 : 0
  hostname = var.tailscale_hostname_cd
  wait_for = "60s"
}

locals {
  ab_tailscale_ip = var.tailscale_nodes_registered ? one([for a in data.tailscale_device.ab[0].addresses : a if startswith(a, "100.")]) : null
  cd_tailscale_ip = var.tailscale_nodes_registered ? one([for a in data.tailscale_device.cd[0].addresses : a if startswith(a, "100.")]) : null
}

resource "tailscale_dns_preferences" "main" {
  magic_dns = true
}

resource "tailscale_dns_split_nameservers" "ab18" {
  count       = var.tailscale_nodes_registered ? 1 : 0
  domain      = "ab18.in"
  nameservers = [local.ab_tailscale_ip]
}

resource "tailscale_dns_split_nameservers" "citrusdental" {
  count       = var.tailscale_nodes_registered ? 1 : 0
  domain      = "citrusdental.in"
  nameservers = [local.cd_tailscale_ip]
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
