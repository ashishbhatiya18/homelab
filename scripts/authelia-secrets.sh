#!/usr/bin/env bash
# Seed Authelia's secrets on a node (idempotent: existing files are kept).
# Run on the node before the first deploy of the stack, e.g. from the Mac:
#
#   ssh dietpi@100.108.211.112 'bash -s -- <username> <email> "<display name>"' < scripts/authelia-secrets.sh
#
# Writes /home/dietpi/localstack/secrets/authelia/ and creates
# /home/dietpi/localstack/data/authelia/. Prints, once, every plaintext the
# operator needs (first-login password, OIDC client secrets).
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

# OIDC clients: plaintext (for the app) next to its PBKDF2 digest (for Authelia).
for client in oidc-test immich; do
  if [[ ! -s $dir/$client.digest ]]; then
    secret=$(rand 64)
    printf '%s' "$secret" >"$dir/$client.secret"
    digest pbkdf2 "$secret" sha512 >"$dir/$client.digest"
    echo "OIDC client $client secret: $secret"
  fi
done

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

# The container runs as root; keep the files private to the owner.
chmod 600 "$dir"/*
echo "secrets in $dir:"
ls -l "$dir"
