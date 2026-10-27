# Firewall: SSH and the game ports are public; txAdmin, Kuma and Netdata only over Tailscale or
# from the listed IPs. Rules are added, never reset, so the owner's own rules survive.
# shellcheck shell=bash
step_access() {
  step "Firewall and admin access"
  if is_true "${TAILSCALE:-}"; then
    command -v tailscale >/dev/null || curl -fsSL https://tailscale.com/install.sh | sh >/dev/null 2>&1 || die "Tailscale didn't install"
    if ! tailscale status --json 2>/dev/null | jq -e '.BackendState=="Running"' >/dev/null; then
      if [ -n "${TAILSCALE_AUTHKEY:-}" ]; then
        tailscale up --authkey "$TAILSCALE_AUTHKEY" --hostname "$(slug "$BRAND_NAME" | tr _ -)" || die "tailscale up failed"
      elif [ "${YES:-0}" = 1 ]; then
        warn "Tailscale isn't logged in: run 'tailscale up' then re-run the installer"
      else
        info "Log this machine into your tailnet (open the link it prints):"
        tailscale up --hostname "$(slug "$BRAND_NAME" | tr _ -)" || die "tailscale up failed"
      fi
    fi
    TS_NAME=$(tailscale status --json 2>/dev/null | jq -r '.Self.DNSName // empty' | sed 's/\.$//')
    [ -n "$TS_NAME" ] && ok "Tailscale: $TS_NAME"
  fi

  if in_container && [ "${FXSHIP_FIREWALL:-auto}" = auto ]; then
    warn "running in a container: firewall left to the host"
    return 0
  fi
  local sshport; sshport=$(sshd -T 2>/dev/null | awk '/^port /{print $2; exit}'); sshport=${sshport:-22}
  ufw allow "$sshport/tcp" comment 'ssh' >/dev/null
  ufw allow "$PROD_GAME_PORT" comment 'fivem prod' >/dev/null
  is_true "$DEV_ENABLED" && [ "$DEV_GAME_PORT" != "$PROD_GAME_PORT" ] && ufw allow "$DEV_GAME_PORT" comment 'fivem dev' >/dev/null
  local p
  for p in 40120 40130 3001 19999; do ufw delete allow "$p/tcp" >/dev/null 2>&1 || true; done
  if is_true "${TAILSCALE:-}"; then
    ufw allow in on tailscale0 comment 'tailnet: admin pages' >/dev/null
    ufw allow 41641/udp comment 'tailscale direct' >/dev/null
  else
    local ip
    for ip in ${ADMIN_ALLOW_IPS//,/ }; do
      for p in 40120 40130 3001 19999; do ufw allow from "$ip" to any port "$p" proto tcp comment 'fxship admin' >/dev/null; done
    done
  fi
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  ufw --force enable >/dev/null
  ok "firewall: SSH ($sshport), game ports public; admin pages $(is_true "${TAILSCALE:-}" && echo "on Tailscale only" || echo "from ${ADMIN_ALLOW_IPS:-nowhere}")"
}
