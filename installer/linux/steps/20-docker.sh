# Docker, for MariaDB and Uptime Kuma. Skipped when nothing needs it.
# shellcheck shell=bash
needs_docker() { is_true "${DB_INSTALL:-}" || is_true "${MONITOR_KUMA:-}"; }

step_docker() {
  needs_docker || return 0
  step "Docker"
  if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
    ok "already installed ($(docker --version | cut -d, -f1))"
    return
  fi
  apt-get install -y -qq docker.io docker-compose-v2 >/dev/null
  systemctl enable --now docker >/dev/null
  ok "$(docker --version | cut -d, -f1)"
}

# compose_up starts the supporting services this config asks for, and stops the ones it doesn't.
compose_up() {
  is_true "${DOKPLOY:-}" && return 0
  needs_docker || return 0
  local dir=$FXSHIP_SHARE/compose profiles=()
  install -d -m 700 "$dir"
  install -m 644 "$BUNDLE/installer/linux/files/compose.yml" "$dir/compose.yml"
  local mem_mb pool
  mem_mb=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
  pool=$(( mem_mb / 8 )); [ $pool -lt 256 ] && pool=256; [ $pool -gt 4096 ] && pool=4096
  umask 077
  cat >"$dir/.env" <<ENV
MARIADB_ROOT_PASSWORD=${MARIADB_ROOT_PASSWORD:-unused}
MARIADB_IMAGE=${MARIADB_IMAGE:-mariadb:12.3}
DB_BUFFER_POOL=${pool}M
TZ=$TZ
ENV
  umask 022
  is_true "${DB_INSTALL:-}" && profiles+=(--profile db)
  is_true "${MONITOR_KUMA:-}" && profiles+=(--profile kuma)
  docker compose -p fxship -f "$dir/compose.yml" --env-file "$dir/.env" "${profiles[@]}" up -d --remove-orphans --quiet-pull 2>&1 | grep -v '^ *$' | sed 's/^/    /' || die "docker compose failed"
}
