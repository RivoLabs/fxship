# Daily restic backups of the databases and everything on the box that isn't in git.
# shellcheck shell=bash
step_backups() {
  install -m 755 "$BUNDLE/installer/linux/files/fxship-backup" /usr/local/sbin/fxship-backup
  if ! is_true "${BACKUP_ENABLED:-}"; then
    systemctl disable --now fxship-backup.timer >/dev/null 2>&1 || true
    return 0
  fi
  step "Backups"
  install -m 644 "$BUNDLE/installer/linux/files/fxship-backup.service" /etc/systemd/system/
  sed "s/@BACKUP_TIME@/${BACKUP_TIME:-04:30}:00/" "$BUNDLE/installer/linux/files/fxship-backup.timer" >/etc/systemd/system/fxship-backup.timer
  systemctl daemon-reload
  systemctl enable --now fxship-backup.timer >/dev/null 2>&1
  info "taking a backup now..."
  if /usr/local/sbin/fxship-backup run >/tmp/fxship-backup.log 2>&1; then
    ok "backup done, then daily at ${BACKUP_TIME:-04:30} to $BACKUP_REPO"
  else
    tail -5 /tmp/fxship-backup.log | sed 's/^/    /'
    warn "the backup failed; fix the location or keys, then: fxship-backup run"
  fi
}
