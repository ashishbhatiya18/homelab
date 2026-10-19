#!/usr/bin/env bash
# Seed Authelia's secrets on a node (idempotent: existing files are kept).
# Run on the node before the first deploy of the stack, e.g. from the Mac:
#
#   ssh dietpi@100.108.211.112 'bash -s -- <username> <email> "<display name>"' < scripts/authelia-secrets.sh
#
# Writes /home/dietpi/localstack/secrets/authelia/ and creates
# /home/dietpi/localstack/data/authelia/. Prints, once, every plaintext the
# operator needs (first-login password, the Immich client secret). Users and
# clients are managed afterwards with `home auth`.
set -euo pipefail

user=${1:?usage: authelia-secrets.sh <username> <email> [display name]}
email=${2:?usage: authelia-secrets.sh <username> <email> [display name]}
name=${3:-$user}

dir=${AUTHELIA_SECRETS:-/home/dietpi/localstack/secrets/authelia}
data=${AUTHELIA_DATA:-/home/dietpi/localstack/data/authelia}
image=authelia/authelia:4.39

mkdir -p "$dir" "$data"
chmod 700 "$dir"
umask 077

authelia() { docker run --rm --user "$(id -u):$(id -g)" -v "$dir:/out" "$image" authelia "$@"; }
rand() { authelia crypto rand --length "${1:-64}" --charset alphanumeric | sed 's/^Random Value: //' | tr -d '\n'; }
digest() { authelia crypto hash generate "$1" --password "$2" ${3:+--variant "$3"} | sed 's/^Digest: //' | tr -d '\n'; }

for f in session_secret storage_encryption_key jwt_secret oidc_hmac_secret; do
  [[ -s $dir/$f ]] || rand 64 >"$dir/$f"
done

if [[ ! -s $dir/oidc_jwks.pem ]]; then
  authelia crypto pair rsa generate --bits 4096 --directory /out >/dev/null
  mv "$dir/private.pem" "$dir/oidc_jwks.pem"
  rm -f "$dir/public.pem"
fi

# OIDC clients live in clients.yml, managed with `home auth client …`.
# Authelia needs at least one to start, so a new node gets Immich's; its
# secret is shown once (only the PBKDF2 digest is kept).
if [[ ! -s $dir/clients.yml ]]; then
  if [[ ! -s $dir/immich.digest ]]; then
    secret=$(rand 64)
    digest pbkdf2 "$secret" sha512 >"$dir/immich.digest"
    echo "OIDC client immich secret: $secret"
  fi
  cat >"$dir/clients.yml" <<'EOF'
# OpenID Connect clients, managed with `home auth client …`.
identity_providers:
  oidc:
    clients:
      - client_id: immich
        client_name: Immich
        client_secret: '{{ secret "/secrets/immich.digest" }}'
        public: false
        authorization_policy: one_factor
        consent_mode: pre-configured
        pre_configured_consent_duration: 1y
        require_pkce: true
        pkce_challenge_method: S256
        redirect_uris:
          - https://photos.ab18.in/auth/login
          - https://photos.ab18.in/user-settings
          - app.immich:///oauth-callback
        scopes: [openid, profile, email]
        response_types: [code]
        grant_types: [authorization_code]
        access_token_signed_response_alg: none
        userinfo_signed_response_alg: none
        token_endpoint_auth_method: client_secret_basic
EOF
fi

if [[ ! -s $dir/users.yml ]]; then
  password=$(rand 32)
  cat >"$dir/users.yml" <<EOF
users:
  $user:
    displayname: '$name'
    password: '$(digest argon2 "$password")'
    email: '$email'
    groups: [admins]
EOF
  echo "Authelia user $user first-login password: $password"
fi

# Authelia runs as dietpi (1000); keep the files private to it.
chmod 600 "$dir"/*
echo "secrets in $dir:"
ls -l "$dir"
