#!/usr/bin/env bash
set -euo pipefail

# Sets up the locked-down `webtty` SSH user and SSH secret key on node `ab`.
# Run on node `ab` as root or via `sudo bash setup-host.sh`.

WEBTTY_USER="webtty"
WORKSPACE_ROOT="/home/dietpi/repos"
SECRET_DIR="/home/dietpi/localstack/secrets/webtty"
SECRET_KEY="${SECRET_DIR}/ssh_key"
SSHD_CONF="/etc/ssh/sshd_config.d/webtty.conf"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "Run as root: sudo bash $0" >&2
  exit 1
fi

if ! id "${WEBTTY_USER}" >/dev/null 2>&1; then
  useradd -m -s /bin/bash "${WEBTTY_USER}"
  passwd -l "${WEBTTY_USER}" >/dev/null 2>&1 || true
  echo "Created user ${WEBTTY_USER}"
fi

# Ensure POSIX ACL tool is present so we can share WORKSPACE_ROOT without
# adding webtty to dietpi's group (which would expose localstack secrets).
if ! command -v setfacl >/dev/null 2>&1; then
  apt-get update && apt-get install -y --no-install-recommends acl
fi

# Allow webtty to traverse /home/dietpi ONLY to reach /home/dietpi/repos.
# Keep /home/dietpi/localstack strictly 0700 to dietpi.
setfacl -m "u:${WEBTTY_USER}:--x" /home/dietpi
if [[ -d "${WORKSPACE_ROOT}" ]]; then
  setfacl -R -m "u:${WEBTTY_USER}:rwX" "${WORKSPACE_ROOT}"
  setfacl -R -d -m "u:${WEBTTY_USER}:rwX" "${WORKSPACE_ROOT}"
fi

install -d -m 0700 -o 1000 -g 1000 "${SECRET_DIR}"
if [[ ! -f "${SECRET_KEY}" ]]; then
  ssh-keygen -t ed25519 -N "" -C "webtty@term2.ab18.in" -f "${SECRET_KEY}"
  chown 1000:1000 "${SECRET_KEY}" "${SECRET_KEY}.pub"
  chmod 0600 "${SECRET_KEY}"
fi

WEBTTY_HOME="$(getent passwd "${WEBTTY_USER}" | cut -d: -f6)"
install -d -m 0700 -o "${WEBTTY_USER}" -g "${WEBTTY_USER}" "${WEBTTY_HOME}/.ssh"
AUTH_KEYS="${WEBTTY_HOME}/.ssh/authorized_keys"
PUB_KEY="$(cat "${SECRET_KEY}.pub")"
ENTRY="restrict,pty ${PUB_KEY}"
if [[ ! -f "${AUTH_KEYS}" ]] || ! grep -Fq "${PUB_KEY}" "${AUTH_KEYS}"; then
  echo "${ENTRY}" >> "${AUTH_KEYS}"
fi
chown "${WEBTTY_USER}:${WEBTTY_USER}" "${AUTH_KEYS}"
chmod 0600 "${AUTH_KEYS}"

cat > "${SSHD_CONF}" <<'EOF'
# Restrict the webtty user to key auth from the internal_bridge Docker subnet.
Match User webtty Address 11.11.11.0/24,127.0.0.1
    PasswordAuthentication no
    PubkeyAuthentication yes
    AllowTcpForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTunnel no
EOF

sshd -t && systemctl reload ssh

# Optional systemd slice resource cap for all sessions belonging to webtty.
WEBTTY_UID="$(id -u "${WEBTTY_USER}")"
install -d /etc/systemd/system/user-"${WEBTTY_UID}".slice.d
cat > /etc/systemd/system/user-"${WEBTTY_UID}".slice.d/limits.conf <<'EOF'
[Slice]
MemoryMax=4G
CPUQuota=200%
TasksMax=256
EOF
systemctl daemon-reload

echo ""
echo "=== Host setup complete ==="
echo "1) Put this SSH_HOST_FINGERPRINT into nodes/ab/webtty/compose.yaml:"
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{print "   " $2}'
echo ""
echo "2) Install claude and agy for user '${WEBTTY_USER}' (one-time):"
echo "   sudo -iu ${WEBTTY_USER} bash -c 'curl -fsSL https://claude.ai/install.sh | bash'"
echo "   # and install agy CLI into ~${WEBTTY_USER}/.local/bin (or /usr/local/bin/agy)"
