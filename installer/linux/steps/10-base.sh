# Packages, time zone, swap, network buffers, SSH hardening, fail2ban, the fivem user.
# shellcheck shell=bash
step_base() {
  step "Base system"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq -o Dpkg::Options::=--force-confold \
    git git-lfs curl jq xz-utils unzip ca-certificates fail2ban ufw mariadb-client lua5.4 restic \
    apache2-utils python3-venv openssh-client openssh-server unattended-upgrades >/dev/null
  git lfs install --system >/dev/null
  ok "packages"

  timedatectl set-timezone "$TZ" 2>/dev/null || ln -sf "/usr/share/zoneinfo/$TZ" /etc/localtime
  ok "time zone $TZ"

  if ! in_container && ! swapon --show | grep -q .; then
    fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile
    grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >>/etc/fstab
    ok "4 GB swap"
  fi
  cat >/etc/sysctl.d/90-fxship.conf <<'CONF'
# Swap only for spikes; bigger UDP buffers for FXServer with many players.
vm.swappiness=10
net.core.rmem_max=16777216
net.core.wmem_max=16777216
net.core.rmem_default=1048576
net.core.wmem_default=1048576
CONF
  sysctl --system >/dev/null 2>&1 || warn "some kernel settings can't be changed here (container?)"

  # Keys only, but never lock anyone out: only when a key login already exists.
  local keys
  keys=$(cat /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys 2>/dev/null | grep -c '^ssh-\|^ecdsa-\|^sk-' || true)
  if [ "${SSH_HARDEN:-true}" = true ] && [ "$keys" -gt 0 ]; then
    cat >/etc/ssh/sshd_config.d/00-fxship.conf <<'CONF'
# fxship: keys only. 00- so it wins over 50-cloud-init.conf (sshd takes the first value).
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
PubkeyAuthentication yes
MaxAuthTries 4
X11Forwarding no
CONF
    sshd -t && systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
    ok "SSH: key logins only"
  else
    warn "SSH passwords left on: no SSH keys found. Add your key to ~/.ssh/authorized_keys and re-run."
  fi

  echo 'Unattended-Upgrade::Automatic-Reboot "false";' >/etc/apt/apt.conf.d/52-fxship
  cat >/etc/fail2ban/jail.d/fxship-sshd.local <<'CONF'
[sshd]
enabled = true
backend = systemd
maxretry = 5
findtime = 10m
bantime = 1h
CONF
  systemctl enable --now fail2ban >/dev/null 2>&1 && systemctl restart fail2ban || warn "fail2ban didn't start"
  ok "security updates (never auto-reboot), fail2ban"

  ensure_fivem_user
  install -d -m 755 /etc/fivem
  ok "fivem user"
}
