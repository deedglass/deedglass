#!/usr/bin/env bash
# bootstrap.sh (S35): prepares a fresh Ubuntu LTS VM for the cloud kit. Idempotent: run it again and
# it changes nothing that is already right. Run as root on the VM, from the repository checkout:
#
#   sudo deploy/cloud/bootstrap.sh
#
# Docker from Docker's own apt repository, a deedglass system user, ufw (22, 80, 443 only), SSH by
# key only, unattended security upgrades, age, rclone and jq, NTP on, /etc/deedglass (0700) and the
# systemd units. It never writes a secret; GO_LIVE.md step 5 is where you type them.
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "✕ Run it as root: sudo $0"; exit 1; }
. /etc/os-release
[ "${ID:-}" = ubuntu ] || { echo "✕ This script is for Ubuntu LTS (found ${PRETTY_NAME:-unknown})."; exit 1; }
KIT="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$KIT/../.." && pwd)"
step() { echo "✓ $*"; }
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ca-certificates curl gnupg ufw unattended-upgrades age jq rclone >/dev/null
step "Packages · age, rclone, jq, ufw, unattended-upgrades"
if ! command -v docker >/dev/null; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable" > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
fi
systemctl enable --now docker >/dev/null
step "Docker · $(docker --version | cut -d, -f1)"
id deedglass >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin deedglass
step "User · deedglass (system, no login)"
ufw default deny incoming >/dev/null; ufw default allow outgoing >/dev/null
for p in 22/tcp 80/tcp 443/tcp 443/udp; do ufw allow "$p" >/dev/null; done
ufw --force enable >/dev/null
step "Firewall · 22, 80, 443 only"
sshd_conf=/etc/ssh/sshd_config.d/10-deedglass.conf
printf 'PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin prohibit-password\n' > "$sshd_conf"
systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
step "SSH · keys only"
dpkg-reconfigure -f noninteractive unattended-upgrades >/dev/null 2>&1 || true
step "Updates · unattended security upgrades on"
timedatectl set-ntp true 2>/dev/null || true
step "Clock · NTP $(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo unknown)"
install -d -m 0700 -o root -g root /etc/deedglass /etc/deedglass/seal /var/backups/deedglass /var/lib/deedglass
chown 65532:65532 /etc/deedglass/seal && chmod 0500 /etc/deedglass/seal
step "Directories · /etc/deedglass (0700), /etc/deedglass/seal (65532, 0500), /var/backups/deedglass"
ln -sfn "$REPO" /opt/deedglass
for u in "$KIT"/systemd/*.service "$KIT"/systemd/*.timer; do install -m 0644 "$u" /etc/systemd/system/; done
systemctl daemon-reload
step "systemd · units installed (enable them in GO_LIVE.md step 9)"
echo "  next  Fill /etc/deedglass/{deedglass,postgres,ops}.env and pg_admin_password from the examples (GO_LIVE.md step 5)."
