# Uptime Kuma (up/down checks) and Netdata (graphs), both optional, alerts to a Discord webhook.
# shellcheck shell=bash
step_monitoring() {
  is_true "${MONITOR_KUMA:-}" || is_true "${MONITOR_NETDATA:-}" || { compose_up; return 0; }
  step "Monitoring"
  if is_true "${MONITOR_KUMA:-}"; then
    KUMA_PASSWORD=${KUMA_PASSWORD:-$(gen_pass 20)}
    set_conf KUMA_PASSWORD "$KUMA_PASSWORD"; remember "Uptime Kuma (admin)" "$KUMA_PASSWORD"
    if is_true "${DOKPLOY:-}"; then
      info "Dokploy: add compose.yml's uptime-kuma service as a compose app; monitors are set up once it answers on :3001."
    else
      compose_up
    fi
    # Kuma answers HTTP well before its socket API is ready on first start; wait for both.
    local n
    for n in $(seq 1 60); do curl -sf -m 3 http://127.0.0.1:3001/ >/dev/null && break; sleep 3; done
    if [ ! -x "$FXSHIP_SHARE/venv/bin/python" ]; then
      python3 -m venv "$FXSHIP_SHARE/venv"
      "$FXSHIP_SHARE/venv/bin/pip" install -q 'python-socketio[client]==5.*' websocket-client
    fi
    env KUMA_PASSWORD="$KUMA_PASSWORD" "$FXSHIP_SHARE/venv/bin/python" "$BUNDLE/installer/linux/files/kuma-setup.py" 2>&1 | sed 's/^/    /' \
      || warn "Kuma setup didn't finish; re-run the installer"
    ok "Uptime Kuma on port 3001 (user admin)"
  else
    compose_up
  fi
  if is_true "${MONITOR_NETDATA:-}"; then
    if ! command -v netdata >/dev/null; then
      curl -fsSL https://get.netdata.cloud/kickstart.sh -o /tmp/netdata-kickstart.sh
      sh /tmp/netdata-kickstart.sh --non-interactive --stable-channel --disable-telemetry --dont-wait >/tmp/netdata-install.log 2>&1 \
        || { tail -5 /tmp/netdata-install.log; warn "Netdata didn't install"; return 0; }
    fi
    if [ -n "${ALERT_WEBHOOK:-}" ]; then
      local f=/etc/netdata/health_alarm_notify.conf
      [ -f $f ] || cp /usr/lib/netdata/conf.d/health_alarm_notify.conf $f 2>/dev/null || true
      [ -f $f ] && sed -i -E "s|^#?\s*SEND_DISCORD=.*|SEND_DISCORD=\"YES\"|; s|^#?\s*DISCORD_WEBHOOK_URL=.*|DISCORD_WEBHOOK_URL=\"$ALERT_WEBHOOK\"|; s|^#?\s*DEFAULT_RECIPIENT_DISCORD=.*|DEFAULT_RECIPIENT_DISCORD=\"alerts\"|" $f
      systemctl restart netdata
    fi
    ok "Netdata on port 19999"
  fi
}
