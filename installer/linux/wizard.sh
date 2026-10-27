# The questions. Every answer lands in /etc/fxship/fxship.env, and on a re-run every question
# offers the saved answer as its default, so re-running the installer is how settings change.
# shellcheck shell=bash
# Answers are saved through set_conf "$v" "${!v}", which shellcheck can't follow.
# shellcheck disable=SC2034

ensure_fivem_user() {
  id fivem >/dev/null 2>&1 || useradd --system --create-home --home-dir "$FIVEM_ROOT" --shell /usr/sbin/nologin fivem
  install -d -o fivem -g fivem -m 755 "$FIVEM_ROOT"
  # Dev runs as fivem-dev, which can't open prod's folder. Its group shares dev's files with the
  # agent (fivem) and staff.
  id fivem-dev >/dev/null 2>&1 || useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin fivem-dev
  id -nG fivem | grep -qw fivem-dev || usermod -aG fivem-dev fivem
}

# GitHub's SSH host keys, from its API, so the first connection can't be intercepted.
github_known_hosts() {
  local kh=$FIVEM_ROOT/.ssh/known_hosts keys
  install -d -o fivem -g fivem -m 700 "$FIVEM_ROOT/.ssh"
  keys=$(curl -sf -m 15 https://api.github.com/meta | jq -r '.ssh_keys[]' 2>/dev/null) || true
  [ -n "$keys" ] || die "couldn't fetch GitHub's SSH host keys from api.github.com"
  sed 's/^/github.com /' <<<"$keys" >"$kh"
  chown fivem:fivem "$kh"; chmod 644 "$kh"
}

as_fivem() { runuser -u fivem -- env GIT_TERMINAL_PROMPT=0 GIT_SSH_COMMAND="$GIT_SSH_COMMAND" "$@"; }

# The box's own IPs, without Docker's and Tailscale's.
public_ips() {
  ip -4 -o addr show scope global | awk '$2 !~ /^(docker|br-|veth|tailscale|lxdbr|virbr)/ {print $4}' | cut -d/ -f1
}

wizard_server() {
  step "Your server"
  ask BRAND_NAME "Server name (shown on Discord cards and in txAdmin)" "My FiveM Server"
  local tz; tz=$(timedatectl show -p Timezone --value 2>/dev/null || echo UTC)
  ask TZ "Time zone for restarts (e.g. Europe/London, America/New_York)" "${TZ:-$tz}"
  [ -e "/usr/share/zoneinfo/$TZ" ] || die "unknown time zone $TZ (see timedatectl list-timezones)"

  echo
  choose TOPOLOGY "How should prod and dev run?" one-ip \
    "one-ip     One public IP: prod on port 30120, dev on 30130 (most servers)" \
    "two-ip     Two public IPs: each server on its own IP, both on 30120" \
    "prod-only  No dev server: approved deploys go straight to prod, untested"
  local ips first second; ips=$(public_ips | tr '\n' ' '); first=${ips%% *}
  second=$(public_ips | sed -n 2p)
  info "${D}IPs on this machine: ${ips:-none found}${N}"
  case $TOPOLOGY in
    one-ip|prod-only)
      ask PUBLIC_IP "Public IP players connect to" "${PUBLIC_IP:-$first}"
      PROD_IP=$PUBLIC_IP; DEV_IP=$PUBLIC_IP; PROD_GAME_PORT=30120
      # Coming from two-ip, dev was on 30120 too: on one IP it needs its own port.
      [ "${DEV_GAME_PORT:-30120}" = 30120 ] && DEV_GAME_PORT=30130
      if [ "$TOPOLOGY" = one-ip ]; then
        ask DEV_GAME_PORT "Dev game port" "$DEV_GAME_PORT"
        [[ $DEV_GAME_PORT =~ ^[0-9]+$ ]] && [ "$DEV_GAME_PORT" != 30120 ] || die "the dev game port must be a number other than prod's 30120"
      fi
      ;;
    two-ip)
      ask PROD_IP "Prod public IP" "${PROD_IP:-$first}"
      ask DEV_IP "Dev public IP (the second IP must already route to this box)" "${DEV_IP:-$second}"
      [ "$PROD_IP" != "$DEV_IP" ] || die "two-ip needs two different IPs"
      PROD_GAME_PORT=30120; DEV_GAME_PORT=30120; PUBLIC_IP=$PROD_IP
      ;;
    *) die "TOPOLOGY must be one-ip, two-ip or prod-only";;
  esac
  DEV_ENABLED=$([ "$TOPOLOGY" = prod-only ] && echo false || echo true)

  ask RESTART_TIMES "Prod scheduled restarts, 24h, comma separated (cold changes go live at these)" "06:00,18:00"
  [[ $RESTART_TIMES =~ ^([0-2][0-9]:[0-5][0-9],?)+$ ]] || die "RESTART_TIMES must look like 06:00,18:00"
  for v in BRAND_NAME TZ TOPOLOGY PUBLIC_IP PROD_IP DEV_IP PROD_GAME_PORT DEV_GAME_PORT DEV_ENABLED RESTART_TIMES; do set_conf "$v" "${!v}"; done
}

wizard_repo() {
  step "Your GitHub repository"
  info "fxship deploys from a GitHub repo holding either your resources folder or your whole"
  info "server-data folder (server.cfg plus resources/). Not in git yet? See the guide's"
  info "\"Put your server in git\" chapter, then come back."
  ask REPO_URL "Repository (owner/name, or its SSH URL)" "$REPO_URL"
  [ -n "$REPO_URL" ] || die "a repository is required"
  if [[ $REPO_URL =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then REPO_URL="git@github.com:${REPO_URL%.git}.git"; fi
  if [[ $REPO_URL =~ ^https://github.com/(.+)$ ]]; then REPO_URL="git@github.com:${BASH_REMATCH[1]%.git}.git"; fi
  [[ $REPO_URL =~ ^git@github.com:.+/.+\.git$ ]] || die "use owner/name or git@github.com:owner/name.git"
  ask REPO_BRANCH "Branch to deploy from" "main"

  ensure_fivem_user
  local key=$FIVEM_ROOT/.ssh/deploy_key
  GIT_SSH_COMMAND="ssh -i $key -o IdentitiesOnly=yes -o UserKnownHostsFile=$FIVEM_ROOT/.ssh/known_hosts -o StrictHostKeyChecking=yes"
  github_known_hosts
  if [ ! -f "$key" ]; then
    runuser -u fivem -- ssh-keygen -q -t ed25519 -N '' -C "fxship@$(hostname)" -f "$key"
  fi
  set_conf REPO_URL "$REPO_URL"; set_conf REPO_BRANCH "$REPO_BRANCH"; set_conf GIT_SSH_COMMAND "$GIT_SSH_COMMAND"

  local web=${REPO_URL#git@github.com:}; web=https://github.com/${web%.git}
  until as_fivem git ls-remote "$REPO_URL" "refs/heads/$REPO_BRANCH" 2>/dev/null | grep -q .; do
    echo
    info "Add this deploy key to the repo: ${B}$web/settings/keys/new${N}"
    info "Title: fxship $(hostname). Tick ${B}Allow write access${N} (staff use it to ship dev edits from Discord)."
    echo; printf '      %s%s%s\n\n' "$C" "$(cat "$key.pub")" "$N"
    [ "${YES:-0}" = 1 ] && die "can't read $REPO_URL ($REPO_BRANCH) yet: add the key above, then run the installer again"
    read -r -p "  Press Enter once it's added (Ctrl+C to stop)..." _ </dev/tty
  done
  ok "the deploy key can read $REPO_URL ($REPO_BRANCH)"

  # Look at the repo before anything is installed from it; once is enough unless it changed.
  if [ -n "${REPO_LAYOUT:-}" ] && [ "$(conf REPO_DETECTED)" = "$REPO_URL#$REPO_BRANCH" ]; then
    ok "layout: $REPO_LAYOUT"
    return
  fi
  local tmp; tmp=$(runuser -u fivem -- mktemp -d)
  info "Fetching the repo to look at it (big repos take a while)..."
  as_fivem git clone -q --depth 1 --branch "$REPO_BRANCH" "$REPO_URL" "$tmp/repo" || die "clone failed"
  echo
  fxship detect "$tmp/repo" | sed 's/^/    /'
  REPO_LAYOUT=$(fxship detect --json "$tmp/repo" | jq -r .layout)
  set_conf REPO_LAYOUT "$REPO_LAYOUT"
  set_conf REPO_DETECTED "$REPO_URL#$REPO_BRANCH"
  if [ "$REPO_LAYOUT" = resources ] && [ -z "${CFG_SOURCE:-}" ] && [ -f "$tmp/repo/server.cfg" ]; then CFG_SOURCE=repo; fi
  rm -rf "$tmp"
}

wizard_cfg() {
  if [ "$REPO_LAYOUT" = server-data ]; then
    set_conf CFG_SOURCE repo
    return
  fi
  step "Server config"
  info "Your repo holds the resources, so server.cfg lives on this machine."
  info "Give the folder of your current server.cfg (the files exec'd from it are copied too),"
  info "or type ${B}starter${N} for a minimal one to edit later."
  ask CFG_SOURCE "Folder with your server.cfg, or starter" "starter"
  if [ "$CFG_SOURCE" != starter ] && [ "$CFG_SOURCE" != repo ]; then
    [ -f "$CFG_SOURCE/server.cfg" ] || die "no server.cfg in $CFG_SOURCE"
    fxship detect "$CFG_SOURCE" >/dev/null 2>&1 || true
  fi
  set_conf CFG_SOURCE "$CFG_SOURCE"
}

wizard_license() {
  step "Cfx.re license keys"
  info "Each running server needs a key from https://portal.cfx.re (Servers > Register)."
  ask_secret PROD_LICENSE_KEY "Prod license key (cfxk_...)"
  [[ ${PROD_LICENSE_KEY:-} == cfxk_* ]] || die "a prod license key starting with cfxk_ is required"
  set_conf PROD_LICENSE_KEY "$PROD_LICENSE_KEY"
  if is_true "$DEV_ENABLED"; then
    info "Dev can use its own key or share prod's. A separate key keeps dev off prod's server listing."
    ask_secret DEV_LICENSE_KEY "Dev license key (Enter to use the prod key)"
    DEV_LICENSE_KEY=${DEV_LICENSE_KEY:-$PROD_LICENSE_KEY}
    set_conf DEV_LICENSE_KEY "$DEV_LICENSE_KEY"
  fi
}

wizard_txadmin() {
  step "txAdmin"
  ask TX_USER "txAdmin admin username (you and fxship sign in with it)" "admin"
  [[ $TX_USER =~ ^[A-Za-z0-9_.-]{3,20}$ ]] || die "username: 3-20 letters, digits, _ . -"
  PROD_TX_PASSWORD=${PROD_TX_PASSWORD:-$(gen_pass 20)}
  set_conf TX_USER "$TX_USER"; set_conf PROD_TX_PASSWORD "$PROD_TX_PASSWORD"
  remember "txAdmin prod ($TX_USER)" "$PROD_TX_PASSWORD"
  if is_true "$DEV_ENABLED"; then
    DEV_TX_PASSWORD=${DEV_TX_PASSWORD:-$(gen_pass 20)}
    set_conf DEV_TX_PASSWORD "$DEV_TX_PASSWORD"
    remember "txAdmin dev ($TX_USER)" "$DEV_TX_PASSWORD"
  fi
  info "Passwords are generated and saved to $CREDS."
  ask FX_BUILD "FXServer build: recommended, latest, or a build number" "recommended"
  set_conf FX_BUILD "$FX_BUILD"
}

wizard_db() {
  step "Database"
  ask_yes DB_INSTALL "Run MariaDB on this machine for the servers?" y
  set_conf DB_INSTALL "$DB_INSTALL"
  is_true "$DB_INSTALL" || { info "Skipped: set mysql_connection_string in each secrets.cfg yourself."; return; }
  local s; s=$(slug "$BRAND_NAME"); s=${s:-fivem}
  ask DB_PROD_NAME "Prod database name" "${s}_prod"
  DB_PROD_USER=${DB_PROD_USER:-${s:0:20}_prod}
  DB_PROD_PASSWORD=${DB_PROD_PASSWORD:-$(gen_pass 24)}
  MARIADB_ROOT_PASSWORD=${MARIADB_ROOT_PASSWORD:-$(gen_pass 28)}
  for v in DB_PROD_NAME DB_PROD_USER DB_PROD_PASSWORD MARIADB_ROOT_PASSWORD; do set_conf "$v" "${!v}"; done
  if is_true "$DEV_ENABLED"; then
    ask DB_DEV_NAME "Dev database name" "${s}_dev"
    [ "$DB_DEV_NAME" != "$DB_PROD_NAME" ] || die "dev and prod need different databases"
    DB_DEV_USER=${DB_DEV_USER:-${s:0:20}_dev}
    DB_DEV_PASSWORD=${DB_DEV_PASSWORD:-$(gen_pass 24)}
    for v in DB_DEV_NAME DB_DEV_USER DB_DEV_PASSWORD; do set_conf "$v" "${!v}"; done
  fi
  ask DB_IMPORT "Import an existing .sql or .sql.gz dump into prod (path, or blank)" ""
  [ -z "$DB_IMPORT" ] || [ -f "$DB_IMPORT" ] || die "no such file: $DB_IMPORT"
  set_conf DB_IMPORT "$DB_IMPORT"
  remember "MariaDB root" "$MARIADB_ROOT_PASSWORD"
}

wizard_discord() {
  step "Discord"
  info "fxship posts release cards with Approve and Reject buttons. Create a bot for your server:"
  info "https://discord.com/developers/applications > New Application > Bot > Reset Token."
  info "Leave this blank to approve from the command line only."
  ask_secret DISCORD_TOKEN "Bot token"
  if [ -z "${DISCORD_TOKEN:-}" ]; then
    set_conf DISCORD_TOKEN ""; set_conf DISCORD_CHANNEL_ID ""
    return
  fi
  local me app guilds n
  me=$(discord /users/@me) || die "Discord rejected that token"
  app=$(jq -r .id <<<"$me")
  ok "bot $(jq -r .username <<<"$me")"
  set_conf DISCORD_TOKEN "$DISCORD_TOKEN"
  guilds=$(discord /users/@me/guilds)
  until [ "$(jq length <<<"$guilds")" -gt 0 ]; do
    [ "${YES:-0}" = 1 ] && die "the bot isn't in any Discord server yet"
    info "Invite the bot to your server, then press Enter:"
    info "${B}https://discord.com/oauth2/authorize?client_id=$app&scope=bot+applications.commands&permissions=117760${N}"
    read -r _ </dev/tty
    guilds=$(discord /users/@me/guilds)
  done
  local gid
  if [ "$(jq length <<<"$guilds")" = 1 ]; then gid=$(jq -r '.[0].id' <<<"$guilds"); else
    jq -r 'to_entries[] | "    \(.key+1)) \(.value.name)"' <<<"$guilds"
    read -r -p "  Which server? " n </dev/tty
    gid=$(jq -r ".[$((n-1))].id" <<<"$guilds")
  fi
  local chans roles
  chans=$(discord "/guilds/$gid/channels" | jq '[.[] | select(.type==0)] | sort_by(.position)')
  if [ "${YES:-0}" != 1 ] || [ -z "${DISCORD_CHANNEL_ID:-}" ]; then
    [ "${YES:-0}" = 1 ] && die "set DISCORD_CHANNEL_ID"
    jq -r 'to_entries[] | "    \(.key+1)) #\(.value.name)"' <<<"$chans"
    read -r -p "  Channel for release cards (a private #deploys is best): " n </dev/tty
    DISCORD_CHANNEL_ID=$(jq -r ".[$((n-1))].id // empty" <<<"$chans")
    [ -n "$DISCORD_CHANNEL_ID" ] || die "no such channel"
  fi
  roles=$(discord "/guilds/$gid/roles" | jq '[.[] | select(.name != "@everyone" and (.managed|not))] | sort_by(-.position)')
  if [ "${YES:-0}" != 1 ]; then
    jq -r 'to_entries[] | "    \(.key+1)) @\(.value.name)"' <<<"$roles"
    read -r -p "  Roles that may approve prod releases, e.g. 1,3 (server admins always can): " n </dev/tty
    DISCORD_APPROVER_ROLE_IDS=$(for i in ${n//,/ }; do jq -r ".[$((i-1))].id // empty" <<<"$roles"; done | paste -sd, -)
    if is_true "$DEV_ENABLED"; then
      read -r -p "  Extra roles that may ship dev edits but not approve (blank for none): " n </dev/tty
      DISCORD_SHIPPER_ROLE_IDS=$(for i in ${n//,/ }; do jq -r ".[$((i-1))].id // empty" <<<"$roles"; done | paste -sd, -)
    fi
  fi
  ask_yes POWERED_BY "Show a small \"Powered by Rivo\" line on cards?" y
  for v in DISCORD_CHANNEL_ID DISCORD_APPROVER_ROLE_IDS DISCORD_SHIPPER_ROLE_IDS POWERED_BY; do set_conf "$v" "${!v:-}"; done
}

wizard_extras() {
  step "Backups"
  info "A nightly copy of both databases, configs, txAdmin data and runtime files, kept with restic"
  info "(7 daily, 4 weekly, 6 monthly). Use storage off this machine: a dead disk takes local backups too."
  ask_yes BACKUP_ENABLED "Turn on backups?" y
  set_conf BACKUP_ENABLED "$BACKUP_ENABLED"
  if is_true "$BACKUP_ENABLED"; then
    info "Where to: ${B}s3:https://s3.amazonaws.com/bucket/fxship${N}, ${B}b2:bucket:fxship${N},"
    info "${B}sftp:user@host:/backups/fxship${N}, or a local folder (not recommended)."
    ask BACKUP_REPO "Backup location" "/var/backups/fxship/restic"
    case $BACKUP_REPO in
      s3:*) ask AWS_ACCESS_KEY_ID "S3 access key id" ""; ask_secret AWS_SECRET_ACCESS_KEY "S3 secret key"
            set_conf AWS_ACCESS_KEY_ID "$AWS_ACCESS_KEY_ID"; set_conf AWS_SECRET_ACCESS_KEY "${AWS_SECRET_ACCESS_KEY:-}";;
      b2:*) ask B2_ACCOUNT_ID "B2 key id" ""; ask_secret B2_ACCOUNT_KEY "B2 application key"
            set_conf B2_ACCOUNT_ID "$B2_ACCOUNT_ID"; set_conf B2_ACCOUNT_KEY "${B2_ACCOUNT_KEY:-}";;
      /*) warn "local backups don't survive losing this machine";;
    esac
    ask BACKUP_TIME "Daily backup time" "04:30"
    RESTIC_PASSWORD=${RESTIC_PASSWORD:-$(gen_pass 32)}
    set_conf BACKUP_REPO "$BACKUP_REPO"; set_conf BACKUP_TIME "$BACKUP_TIME"; set_conf RESTIC_PASSWORD "$RESTIC_PASSWORD"
    remember "Backup encryption password (without it backups can't be restored, keep a copy elsewhere)" "$RESTIC_PASSWORD"
  fi

  step "Monitoring"
  ask_yes MONITOR_KUMA "Uptime Kuma (up/down checks for both servers, txAdmin and the agent)?" y
  ask_yes MONITOR_NETDATA "Netdata (CPU, memory, disk and network graphs and alerts)?" n
  ALERT_WEBHOOK=${ALERT_WEBHOOK:-}
  if is_true "$MONITOR_KUMA" || is_true "$MONITOR_NETDATA"; then
    ask ALERT_WEBHOOK "Discord webhook for alerts (Channel settings > Integrations > Webhooks; blank for none)" ""
  fi
  for v in MONITOR_KUMA MONITOR_NETDATA ALERT_WEBHOOK; do set_conf "$v" "${!v}"; done

  step "Admin access"
  info "txAdmin, Kuma and Netdata are safest reachable only over Tailscale, a free private network"
  info "for you and your staff. Without it they are limited to the IPs you list."
  ask_yes TAILSCALE "Use Tailscale for admin pages?" y
  set_conf TAILSCALE "$TAILSCALE"
  if ! is_true "$TAILSCALE"; then
    local mine=${SSH_CLIENT:-}; mine=${mine%% *}
    ask ADMIN_ALLOW_IPS "IPs allowed to reach admin pages, comma separated" "${ADMIN_ALLOW_IPS:-$mine}"
    set_conf ADMIN_ALLOW_IPS "$ADMIN_ALLOW_IPS"
  fi
  ask_yes DOKPLOY "Is Dokploy installed here (MariaDB and Kuma go in Dokploy instead)?" n
  set_conf DOKPLOY "$DOKPLOY"
}

wizard() {
  load_conf
  echo
  printf '%sfxship setup%s  %s(Enter keeps the value in brackets)%s\n' "$B" "$N" "$D" "$N"
  wizard_server
  wizard_repo
  wizard_cfg
  wizard_license
  wizard_txadmin
  wizard_db
  wizard_discord
  wizard_extras
  API_TOKEN=${API_TOKEN:-$(gen_pass 40)}
  set_conf API_TOKEN "$API_TOKEN"
  load_conf
}
