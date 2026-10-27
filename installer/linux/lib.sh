# Shared helpers for the Linux installer. Sourced, never run.
# shellcheck shell=bash

FXSHIP_ETC=/etc/fxship
FXSHIP_ENV=${FXSHIP_ENV:-$FXSHIP_ETC/fxship.env}
FIVEM_ROOT=${FIVEM_ROOT:-/srv/fivem}
FXSHIP_SHARE=${FXSHIP_SHARE:-/opt/fxship}
CREDS=/root/fxship-credentials.txt

if [ -t 1 ]; then
  B=$'\e[1m' D=$'\e[2m' R=$'\e[31m' G=$'\e[32m' Y=$'\e[33m' C=$'\e[36m' N=$'\e[0m'
else
  B='' D='' R='' G='' Y='' C='' N=''
fi

step() { printf '\n%s==> %s%s\n' "$B$C" "$*" "$N"; }
info() { printf '    %s\n' "$*"; }
ok()   { printf '    %s✓%s %s\n' "$G" "$N" "$*"; }
warn() { printf '    %s!%s %s\n' "$Y" "$N" "$*" >&2; }
die()  { printf '\n%s✗ %s%s\n' "$R" "$*" "$N" >&2; exit 1; }

# conf KEY prints the current value of KEY from the env file (empty if unset).
conf() {
  [ -f "$FXSHIP_ENV" ] || return 0
  local line
  line=$(grep -E "^$1=" "$FXSHIP_ENV" | tail -1) || true
  line=${line#*=}
  line=${line%\"}; line=${line#\"}
  printf '%s' "$line"
}

# set_conf KEY VALUE writes KEY to the env file, replacing any earlier value. Values are stored
# double-quoted so both the agent and `source` read them the same way.
set_conf() {
  local k=$1 v=$2 tmp
  install -d -m 700 "$FXSHIP_ETC"
  [ -f "$FXSHIP_ENV" ] || install -m 600 /dev/null "$FXSHIP_ENV"
  case $v in *'"'*|*$'\n'*) die "$k: values can't contain double quotes or newlines";; esac
  tmp=$(mktemp "$FXSHIP_ETC/.env.XXXXXX")
  grep -vE "^$k=" "$FXSHIP_ENV" >"$tmp" || true
  printf '%s="%s"\n' "$k" "$v" >>"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$FXSHIP_ENV"
}

# Every setting the installer knows. Unset ones start empty, so `set -u` only catches typos.
SETTINGS="BRAND_NAME TZ TOPOLOGY PUBLIC_IP PROD_IP DEV_IP PROD_GAME_PORT DEV_GAME_PORT DEV_ENABLED RESTART_TIMES
REPO_URL REPO_BRANCH REPO_LAYOUT REPO_DETECTED GIT_SSH_COMMAND CFG_SOURCE PROD_LICENSE_KEY DEV_LICENSE_KEY
TX_USER PROD_TX_PASSWORD DEV_TX_PASSWORD FX_BUILD DB_INSTALL DB_PROD_NAME DB_PROD_USER DB_PROD_PASSWORD
DB_DEV_NAME DB_DEV_USER DB_DEV_PASSWORD DB_IMPORT MARIADB_ROOT_PASSWORD MARIADB_IMAGE DISCORD_TOKEN
DISCORD_CHANNEL_ID DISCORD_APPROVER_ROLE_IDS DISCORD_SHIPPER_ROLE_IDS POWERED_BY BACKUP_ENABLED BACKUP_REPO
BACKUP_TIME RESTIC_PASSWORD AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY B2_ACCOUNT_ID B2_ACCOUNT_KEY
MONITOR_KUMA MONITOR_NETDATA ALERT_WEBHOOK KUMA_PASSWORD TAILSCALE TAILSCALE_AUTHKEY ADMIN_ALLOW_IPS DOKPLOY
API_TOKEN DEV_CPU_CORES DEV_MEMORY_MAX_MB TS_NAME PROD_WORKTREE DEV_WORKTREE"
for _v in $SETTINGS; do : "${!_v:=}"; done
unset _v

# load_conf exports every setting in the env file into this shell.
load_conf() {
  [ -f "$FXSHIP_ENV" ] || return 0
  set -a
  # shellcheck disable=SC1090
  . "$FXSHIP_ENV"
  set +a
}

# gen_pass N: N random letters and digits. Reads a fixed amount first: `tr </dev/urandom | head`
# dies of SIGPIPE under pipefail.
gen_pass() {
  local p
  p=$(head -c 1024 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9')
  printf '%s' "${p:0:${1:-24}}"
}

# ask VAR "Question" [default]: prompts on the terminal unless non-interactive, where the existing
# value or the default is taken as is.
ask() {
  local var=$1 q=$2 def=${3:-} cur ans
  cur=${!var:-$def}
  if [ "${YES:-0}" = 1 ]; then printf -v "$var" '%s' "$cur"; return; fi
  read -r -p "  $q${cur:+ ${D}[$cur]${N}}: " ans </dev/tty || true
  printf -v "$var" '%s' "${ans:-$cur}"
}

# ask_secret VAR "Question": hidden input; Enter keeps the current value.
ask_secret() {
  local var=$1 q=$2 ans
  if [ "${YES:-0}" = 1 ]; then return; fi
  read -r -s -p "  $q${!var:+ ${D}[keep current]${N}}: " ans </dev/tty || true
  echo
  [ -n "$ans" ] && printf -v "$var" '%s' "$ans"
  return 0
}

# ask_yes VAR "Question" default(y|n)
ask_yes() {
  local var=$1 q=$2 def=${3:-y} cur ans
  cur=${!var:-}
  case $cur in true|yes|y|1) def=y;; false|no|n|0) def=n;; esac
  if [ "${YES:-0}" = 1 ]; then ans=$def; else
    read -r -p "  $q ${D}[$( [ "$def" = y ] && echo Y/n || echo y/N )]${N}: " ans </dev/tty || true
    ans=${ans:-$def}
  fi
  case $ans in y|Y|yes|YES) printf -v "$var" true;; *) printf -v "$var" false;; esac
}

# choose VAR "Question" default option1 option2 ...: numbered menu, stores the chosen option.
choose() {
  local var=$1 q=$2 def=$3; shift 3
  local i=1 o ans cur=${!var:-$def} tag
  if [ "${YES:-0}" = 1 ]; then printf -v "$var" '%s' "$cur"; return; fi
  tag=$([ -n "${!var:-}" ] && echo current || echo default)
  echo "  $q"
  for o in "$@"; do
    printf '    %s%d)%s %s%s\n' "$B" "$i" "$N" "$o" "$( [ "${o%% *}" = "$cur" ] && echo " ${D}($tag, Enter)${N}")"
    i=$((i+1))
  done
  while :; do
    read -r -p "  Choose 1-$#: " ans </dev/tty || true
    [ -z "$ans" ] && { printf -v "$var" '%s' "$cur"; return; }
    if [[ $ans =~ ^[0-9]+$ ]] && [ "$ans" -ge 1 ] && [ "$ans" -le $# ]; then
      o=${!ans}; printf -v "$var" '%s' "${o%% *}"; return
    fi
  done
}

# remember LABEL VALUE adds a line to the root-only credentials file.
remember() {
  [ -f "$CREDS" ] || install -m 600 /dev/null "$CREDS"
  local tmp; tmp=$(mktemp)
  grep -vF "$1:" "$CREDS" >"$tmp" || true
  printf '%s: %s\n' "$1" "$2" >>"$tmp"
  install -m 600 "$tmp" "$CREDS"; rm -f "$tmp"
}

is_true() { case ${1:-} in true|yes|y|1) return 0;; esac; return 1; }

# in_container: LXD, Docker and friends can't load kernel modules or set some sysctls.
in_container() { systemd-detect-virt --container --quiet 2>/dev/null; }

# Game servers that are up have players to protect: never restart one from the installer.
game_up() { curl -sf -m 3 "$1" >/dev/null 2>&1; }

slug() { tr '[:upper:]' '[:lower:]' <<<"$1" | tr -c 'a-z0-9\n' '_' | sed 's/__*/_/g; s/^_//; s/_$//' | cut -c1-24; }

# discord GET PATH: Discord API call with the configured bot token.
discord() { curl -sf -m 15 -H "Authorization: Bot $DISCORD_TOKEN" "https://discord.com/api/v10$1"; }
