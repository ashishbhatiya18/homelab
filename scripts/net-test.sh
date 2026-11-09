#!/usr/bin/env bash
# Network test suite: DNS answers per view (LAN, Tailscale, public) and the
# three ingress paths (LAN, Tailscale, Cloudflare tunnel) to Traefik on ab.
# Host lists come from the repo: Traefik Host() labels, cloud/tunnels/*.yaml
# and the cd allowlist in dnsdist.conf, so new services are covered unchanged.
#
# Run from the Mac on the home LAN; Tailscale up for the Tailscale checks
# (skipped, not passed, when it is down):
#   scripts/net-test.sh                        # LAN DNS = Pi-hole
#   LAN_DNS=10.10.10.12 scripts/net-test.sh    # LAN DNS = dnsdist (after Blocky)
#   PUBLIC_IP=x.x.x.x scripts/net-test.sh      # from OUTSIDE the LAN: open ports
# Exit status is the number of failures.
set -u
cd "$(dirname "$0")/.." || exit

LAN_DNS=${LAN_DNS:-10.10.10.10}
TS_DNS=${TS_DNS:-100.116.167.96}
AB_LAN=10.10.10.11 AB_TS=100.108.211.112 CD_LAN=10.10.10.12 CD_TS=100.116.167.96
CURL=/usr/bin/curl # Homebrew curl has no macOS Local Network permission

pass=0 fail=0 skip=0
ok()      { pass=$((pass + 1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad()     { fail=$((fail + 1)); printf '  \033[31m✗\033[0m %s — %s\n' "$1" "$2"; }
skipped() { skip=$((skip + 1)); printf '  \033[33m-\033[0m %s — skipped: %s\n' "$1" "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
check()   { if [[ $2 == "$3" ]]; then ok "$1"; else bad "$1" "got '${2}', want '$3'"; fi; }

# Final A/AAAA/MX… answers only (CNAME chain dropped), sorted, one line.
q() { dig +time=2 +tries=1 +short "@$1" "$2" "${3:-A}" | grep -vE '^;|\.$' | sort | xargs; }
# "STATUS ANSWERCOUNT", e.g. "NOERROR 0" for an empty answer.
rc() { dig +time=2 +tries=1 "@$1" "$2" "${3:-A}" ${4:+"$4"} | grep -oE 'status: [A-Z]+|ANSWER: [0-9]+' | cut -d' ' -f2 | xargs; }
private='^(0\.0\.0\.0|127\.|10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.)'
is_public() { # name answers
  if [[ -z $2 ]]; then bad "$1" "no answer"
  elif tr ' ' '\n' <<<"$2" | grep -qE "$private"; then bad "$1" "private answer '$2'"
  else ok "$1"; fi
}

# --- host lists from the repo ------------------------------------------------
labels() { grep -rhE '^\s*- traefik\.http\.routers\.[^.]+\.(rule|middlewares)=' nodes/ab; }
# shellcheck disable=SC2016 # literal backticks, as in Traefik Host() rules
AB_HOSTS=$(labels | grep -oE 'Host\(`[^`]+`\)' | sed -E 's/Host\(`(.*)`\)/\1/' | sort -u)
AUTH_HOSTS=$(labels | sed -E 's/.*routers\.([^.]+)\.(rule|middlewares)=(.*)/\1 \2 \3/' | awk '
  $2 == "rule" && match($3, /`[^`]+`/) { host[$1] = substr($3, RSTART + 1, RLENGTH - 2) }
  $2 == "middlewares" && $3 ~ /(^|,)auth(@[a-z]+)?(,|$)/ { auth[$1] = 1 }
  END { for (r in auth) if (r in host) print host[r] }' | sort -u)
TUNNEL_ALL=$(grep -hoE '^\s*- (hostname: )?[a-z0-9.-]+\.[a-z]+$' cloud/tunnels/*.yaml | awk '{print $NF}' | sort -u)
TUNNEL_AB=$(grep -hoE '^\s*- [a-z0-9.-]+\.ab18\.in$' cloud/tunnels/ab18-localstack.yaml | awk '{print $NF}' | sort -u)
INTERNAL=$(comm -23 <(echo "$AB_HOSTS") <(echo "$TUNNEL_ALL"))
CD_ALLOW=$(grep -oE 'ipairs\(\{[^}]*\}' nodes/cd/network/config/dnsdist/dnsdist.conf | grep -oE '"[a-z0-9-]+"' | tr -d '"' | sed 's/$/.citrusdental.in/')
CD_ALLOW_INTERNAL=$(comm -23 <(echo "$CD_ALLOW" | sort) <(echo "$TUNNEL_ALL"))

# --- LAN DNS -------------------------------------------------------------------
section "LAN DNS (@$LAN_DNS)"
iface=$(route -n get default 2>/dev/null | awk '/interface:/ {print $2}')
dhcp=$(ipconfig getpacket "${iface:-en0}" 2>/dev/null | sed -nE 's/.*domain_name_server.*\{(.*)\}/\1/p')
check "DHCP hands out $LAN_DNS as the DNS server" "$dhcp" "$LAN_DNS"
for h in $AB_HOSTS; do check "$h → ab LAN" "$(q "$LAN_DNS" "$h")" "$AB_LAN"; done
check "unknown name under ab18.in → ab LAN (wildcard)" "$(q "$LAN_DNS" "nope-$$.ab18.in")" "$AB_LAN"
check "photos.ab18.in AAAA empty" "$(rc "$LAN_DNS" photos.ab18.in AAAA)" "NOERROR 0"
check "photos.ab18.in HTTPS empty" "$(rc "$LAN_DNS" photos.ab18.in TYPE65)" "NOERROR 0"
for h in $CD_ALLOW; do check "$h → cd LAN" "$(q "$LAN_DNS" "$h")" "$CD_LAN"; done
is_public "apex ab18.in resolves publicly (Pages)" "$(q "$LAN_DNS" ab18.in)"
if [[ $(dig +short "@$LAN_DNS" ab18.in MX) == *route1.mx.cloudflare.net* ]]; then ok "ab18.in MX is Cloudflare mail"; else bad "ab18.in MX is Cloudflare mail" "missing"; fi
is_public "www.citrusdental.in resolves publicly" "$(q "$LAN_DNS" www.citrusdental.in)"
is_public "api.citrusdental.in resolves publicly (tunnel)" "$(q "$LAN_DNS" api.citrusdental.in)"
is_public "google.com resolves" "$(q "$LAN_DNS" google.com)"
check "DNS over TCP works" "$(rc "$LAN_DNS" google.com A +tcp | cut -d' ' -f1)" "NOERROR"
check "ads blocked (doubleclick.net)" "$(q "$LAN_DNS" doubleclick.net)" "0.0.0.0"
check "DNSSEC: bogus signature refused (dnssec-failed.org)" "$(rc "$LAN_DNS" dnssec-failed.org | cut -d' ' -f1)" "SERVFAIL"

# --- Tailscale DNS -------------------------------------------------------------
section "Tailscale DNS (@$TS_DNS)"
if [[ -z $(rc "$TS_DNS" ab18.in) ]]; then
  skipped "all Tailscale DNS checks" "$TS_DNS unreachable (Tailscale down?)"
else
  for h in $AB_HOSTS; do check "$h → ab Tailscale" "$(q "$TS_DNS" "$h")" "$AB_TS"; done
  check "photos.ab18.in AAAA empty" "$(rc "$TS_DNS" photos.ab18.in AAAA)" "NOERROR 0"
  check "photos.ab18.in HTTPS empty" "$(rc "$TS_DNS" photos.ab18.in TYPE65)" "NOERROR 0"
  for h in $CD_ALLOW; do check "$h → cd Tailscale" "$(q "$TS_DNS" "$h")" "$CD_TS"; done
  is_public "apex ab18.in resolves publicly" "$(q "$TS_DNS" ab18.in)"
  is_public "www.citrusdental.in resolves publicly" "$(q "$TS_DNS" www.citrusdental.in)"
  is_public "google.com resolves" "$(q "$TS_DNS" google.com)"
fi

# --- public DNS: only tunnel hosts exist --------------------------------------
section "Public DNS (@1.1.1.1)"
for h in $TUNNEL_ALL; do is_public "$h is published" "$(q 1.1.1.1 "$h")"; done
for h in $INTERNAL $CD_ALLOW_INTERNAL; do check "$h is not published" "$(q 1.1.1.1 "$h")" ""; done

# --- ingress ---------------------------------------------------------------------
# "code location cf-ray?" for GET https://host/ sent to ip; curl checks the cert.
http() {
  $CURL -s -o /dev/null -D - --max-time 8 --resolve "$1:443:$2" "https://$1/" | tr -d '\r' | awk '
    NR == 1 { code = $2 } tolower($1) == "location:" { loc = $2 } tolower($1) == "cf-ray:" { cf = 1 }
    END { print (code ? code : "000"), (loc ? loc : "-"), (cf ? 1 : 0) }'
}
# name host ip want_cf: reached over TLS, no 5xx, Authelia in front where labelled.
expect_ingress() {
  local code loc cf
  read -r code loc cf <<<"$(http "$2" "$3")"
  if [[ $code == 000 ]]; then bad "$1" "no TLS connection (cert or connect failure)"
  elif ((code >= 500)); then bad "$1" "HTTP $code"
  elif [[ $cf != "$4" ]]; then bad "$1" "cf-ray header $([[ $cf == 1 ]] && echo present || echo missing): wrong path"
  elif grep -qx "$2" <<<"$AUTH_HOSTS" && ! [[ $code == 401 || ($code == 30[0-9] && $loc == https://id.ab18.in*) ]]; then
    bad "$1" "HTTP $code without Authelia login"
  else ok "$1 (HTTP $code)"; fi
}

section "Ingress over the LAN ($AB_LAN)"
for h in $AB_HOSTS; do expect_ingress "$h" "$h" "$AB_LAN" 0; done

section "Ingress over Tailscale ($AB_TS)"
if ! nc -z -G 3 "$AB_TS" 443 2>/dev/null; then
  skipped "all Tailscale ingress checks" "$AB_TS:443 unreachable (Tailscale down?)"
else
  for h in $AB_HOSTS; do expect_ingress "$h" "$h" "$AB_TS" 0; done
fi

section "Ingress through the Cloudflare tunnel"
for h in $TUNNEL_AB; do
  ip=$(q 1.1.1.1 "$h" | awk '{print $1}')
  if [[ -z $ip ]]; then bad "$h" "not published"; else expect_ingress "$h" "$h" "$ip" 1; fi
done
# Internal hosts forced through Cloudflare must never reach an app.
cf_ip=$(q 1.1.1.1 "$(head -1 <<<"$TUNNEL_AB")" | awk '{print $1}')
for h in $INTERNAL; do
  read -r code _ _ <<<"$(http "$h" "$cf_ip")"
  if [[ $code =~ ^(2|3) || $code == 401 ]]; then bad "$h unreachable via Cloudflare" "HTTP $code"; else ok "$h unreachable via Cloudflare (HTTP $code)"; fi
done

# --- exposure from the internet (run off-LAN) --------------------------------
section "Open ports on the home IP"
if [[ -z ${PUBLIC_IP:-} ]]; then
  skipped "open-port checks" "set PUBLIC_IP and run from outside the LAN"
else
  for p in 22 53 80 443; do
    if nc -z -G 3 "$PUBLIC_IP" "$p" 2>/dev/null; then bad "TCP $p closed" "open"; else ok "TCP $p closed"; fi
  done
  check "no open resolver on UDP 53" "$(rc "$PUBLIC_IP" google.com)" ""
fi

printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skip"
exit "$fail"
