# The deploy agent: binary, service, and the first checkout of the repo onto both servers.
# shellcheck shell=bash
step_agent() {
  step "fxship agent"
  install -d -o fivem -g fivem "$FIVEM_ROOT/deploy"
  # Worktree paths for the agent, matching the layout.
  if [ "$REPO_LAYOUT" = server-data ]; then
    set_conf PROD_WORKTREE "$FIVEM_ROOT/prod/server-data"; set_conf DEV_WORKTREE "$FIVEM_ROOT/dev/server-data"
  else
    set_conf PROD_WORKTREE "$FIVEM_ROOT/prod/server-data/resources"; set_conf DEV_WORKTREE "$FIVEM_ROOT/dev/server-data/resources"
  fi
  set_conf PROD_INFO_URL "http://127.0.0.1:$PROD_GAME_PORT/info.json"
  set_conf DEV_INFO_URL "http://127.0.0.1:$DEV_GAME_PORT/info.json"
  if [ "$TOPOLOGY" = two-ip ]; then
    set_conf PROD_INFO_URL "http://$PROD_IP:$PROD_GAME_PORT/info.json"; set_conf DEV_INFO_URL "http://$DEV_IP:$DEV_GAME_PORT/info.json"
  fi
  set_conf FIVEM_ROOT "$FIVEM_ROOT"
  load_conf

  install -m 644 "$BUNDLE/installer/linux/files/fxship.service" /etc/systemd/system/fxship.service
  sed -i "s#/srv/fivem#$FIVEM_ROOT#g" /etc/systemd/system/fxship.service
  systemctl daemon-reload
  systemctl enable fxship >/dev/null 2>&1
  systemctl restart fxship
  info "fetching the repo onto the servers (a large repo takes a while the first time)..."
  local n=0 sha
  while :; do
    sha=$(fxship status 2>/dev/null | jq -r '.prodSha // empty' 2>/dev/null || true)
    [ -n "$sha" ] && break
    systemctl is-active --quiet fxship || { journalctl -u fxship -n 20 --no-pager | sed 's/^/    /'; die "the agent stopped"; }
    n=$((n+1))
    if [ $((n % 10)) = 0 ]; then info "still fetching: $(du -sh "$FIVEM_ROOT/repo.git" 2>/dev/null | cut -f1) so far"; fi
    sleep 3
  done
  ok "prod is on ${sha:0:7}"
  # The dev worktree root is where staff edit, so it gets the permissions fix-up timer.
  if is_true "$DEV_ENABLED"; then
    install -m 755 "$BUNDLE/installer/linux/files/fxship-dev-perms" /usr/local/sbin/fxship-dev-perms
    install -m 644 "$BUNDLE/installer/linux/files/fxship-dev-perms.service" "$BUNDLE/installer/linux/files/fxship-dev-perms.timer" /etc/systemd/system/
    systemctl daemon-reload
    /usr/local/sbin/fxship-dev-perms
    systemctl enable --now fxship-dev-perms.timer >/dev/null 2>&1
    git -c safe.directory='*' -C "$DEV_WORKTREE" status --porcelain >/dev/null 2>&1 || true
    ok "dev files are editable by staff (fxship-add-staff)"
  else
    systemctl disable --now fxship-dev-perms.timer >/dev/null 2>&1 || true
  fi
  install -m 755 "$BUNDLE/installer/linux/files/fxship-add-staff" "$BUNDLE/installer/linux/files/fxship-remove-staff" /usr/local/sbin/
}
