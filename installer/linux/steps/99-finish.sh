# The summary: where everything is, what to do next, and a full doctor run.
# shellcheck shell=bash
step_finish() {
  step "Done"
  local host=${TS_NAME:-$PUBLIC_IP}
  echo
  printf '  %sPlayers connect to%s  %s:%s%s\n' "$B" "$N" "$PROD_IP" "$PROD_GAME_PORT" "$( [ "$PROD_GAME_PORT" = 30120 ] && echo " (or just $PROD_IP)")"
  is_true "$DEV_ENABLED" && printf '  %sDev server%s          %s:%s\n' "$B" "$N" "$DEV_IP" "$DEV_GAME_PORT"
  printf '  %stxAdmin prod%s        http://%s:40120  (user %s)\n' "$B" "$N" "$host" "$TX_USER"
  is_true "$DEV_ENABLED" && printf '  %stxAdmin dev%s         http://%s:40130\n' "$B" "$N" "$host"
  is_true "${MONITOR_KUMA:-}" && printf '  %sUptime Kuma%s         http://%s:3001  (user admin)\n' "$B" "$N" "$host"
  printf '  %sPasswords%s           %s (root only)\n' "$B" "$N" "$CREDS"
  printf '  %sSettings%s            %s, then re-run the installer\n' "$B" "$N" "$FXSHIP_ENV"
  echo
  info "Push to $REPO_BRANCH and a release card appears in Discord within a minute."
  is_true "$DEV_ENABLED" && info "Give staff a login for editing dev: fxship-add-staff <name> '<ssh public key>'"
  info "Commands: fxship status | fxship doctor | fxship-backup list"
  echo
  step "Checking everything (fxship doctor)"
  fxship doctor || warn "fix the items above, then run: fxship doctor"
}
