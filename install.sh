#!/usr/bin/env bash
# fxship installer for Ubuntu 24.04. Safe to run again: every step checks what is already there,
# and a re-run is how settings change.
#
#   sudo bash install.sh                  interactive setup (or change settings)
#   sudo bash install.sh --config FILE    use the answers in FILE (fxship.env format), no questions
#   sudo bash install.sh --upgrade        re-apply everything with the saved answers (fxship upgrade)
#   curl -fsSL https://github.com/RivoLabs/fxship/releases/latest/download/install.sh | sudo bash
set -euo pipefail

YES=0 CONFIG=""
while [ $# -gt 0 ]; do
  case $1 in
    --config) CONFIG=${2:?--config needs a file}; YES=1; shift ;;
    --upgrade|--yes|-y) YES=1 ;;
    -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done
export YES
# Ubuntu's needrestart would otherwise prompt or print a wall of text after every apt install.
export NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1 DEBIAN_FRONTEND=noninteractive

[ "$(id -u)" = 0 ] || { echo "run it as root: sudo bash install.sh" >&2; exit 1; }
[ "$(uname -m)" = x86_64 ] || { echo "FXServer needs an x86_64 machine" >&2; exit 1; }
# shellcheck disable=SC1091
. /etc/os-release
if [ "${ID:-}" != ubuntu ] || [ "${VERSION_ID:-}" != 24.04 ]; then
  [ "${FXSHIP_ANY_OS:-0}" = 1 ] || { echo "fxship supports Ubuntu 24.04 (this is ${PRETTY_NAME:-unknown}); set FXSHIP_ANY_OS=1 to try anyway" >&2; exit 1; }
fi

# Piped from curl: fetch the release bundle and run its installer instead.
BUNDLE=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)
if [ -z "$BUNDLE" ] || [ ! -f "$BUNDLE/installer/linux/lib.sh" ]; then
  command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl; }
  tag=$(curl -fsSL https://api.github.com/repos/RivoLabs/fxship/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
  [ -n "$tag" ] || { echo "couldn't find the latest fxship release" >&2; exit 1; }
  dir=/opt/fxship/releases/$tag
  mkdir -p "$dir"
  curl -fsSL "https://github.com/RivoLabs/fxship/releases/download/$tag/fxship_${tag#v}_linux_amd64.tar.gz" | tar -xz -C "$dir" --strip-components=1
  exec bash "$dir/install.sh" "$@" </dev/tty
fi
export BUNDLE

# shellcheck source=installer/linux/lib.sh
. "$BUNDLE/installer/linux/lib.sh"
for f in "$BUNDLE"/installer/linux/wizard.sh "$BUNDLE"/installer/linux/steps/*.sh; do
  # shellcheck disable=SC1090
  . "$f"
done

exec > >(tee -a /var/log/fxship-install.log) 2>&1
echo "--- fxship install $(date -Is)"

# The binary comes with the bundle; from a source checkout it is built (needs Go).
if [ -x "$BUNDLE/bin/fxship" ]; then
  install -m 755 "$BUNDLE/bin/fxship" /usr/local/bin/fxship
elif command -v go >/dev/null; then
  (cd "$BUNDLE/agent" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git -C "$BUNDLE" describe --tags --always 2>/dev/null || echo dev)" -o /usr/local/bin/fxship .)
else
  die "no bin/fxship in $BUNDLE: use a release bundle, or install Go to build from source"
fi
ok "$(fxship version)"

# --config merges its answers into the saved settings: passwords generated on an earlier run stay.
if [ -n "$CONFIG" ] && [ "$(readlink -f "$CONFIG")" != "$(readlink -f "$FXSHIP_ENV")" ]; then
  [ -f "$CONFIG" ] || die "no such file: $CONFIG"
  while IFS= read -r line; do
    case $line in ''|'#'*) continue ;; esac
    k=${line%%=*}; v=${line#*=}; v=${v%\"}; v=${v#\"}
    [[ $k =~ ^[A-Z][A-Z0-9_]*$ ]] || die "$CONFIG: bad line: $k"
    set_conf "$k" "$v"
  done <"$CONFIG"
fi
if [ "$YES" = 1 ] && [ ! -f "$FXSHIP_ENV" ]; then
  die "no saved settings in $FXSHIP_ENV: run without --upgrade/--yes first, or pass --config FILE"
fi
# Non-interactive runs go through the same questions, each answered by the saved value, so
# generated passwords and checks are the same either way.
wizard
install -d -m 755 "$FXSHIP_SHARE"
ln -sfn "$BUNDLE" "$FXSHIP_SHARE/current" 2>/dev/null || true

step_base
step_access
step_docker
step_database
step_fxserver
step_servers
step_agent
start_servers
step_backups
step_monitoring
step_finish
