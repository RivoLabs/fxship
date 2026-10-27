# The FXServer artifact (it includes txAdmin), shared by both servers.
# shellcheck shell=bash
fx_url() {
  local build=$1 api=https://changelogs-live.fivem.net/api/changelog/versions/linux/server
  case $build in
    recommended|latest|optional) curl -sf -m 20 "$api" | jq -r ".${build}_download" ;;
    *[!0-9]*) return 1 ;;
    # Older builds are listed on Cfx's download page (the artifact index now redirects there).
    *) curl -sfL -m 30 'https://docs.fivem.net/docs/server-download/?platform=legacy&os=linux' \
         | grep -oE "https://runtime\.fivem\.net/artifacts/fivem/build_proot_linux/master/$build-[0-9a-f]+/fx\.tar\.xz" | head -1 ;;
  esac
}

step_fxserver() {
  step "FXServer"
  local url build dest
  url=$(fx_url "${FX_BUILD:-recommended}") || true
  [ -n "$url" ] && [ "$url" != null ] || die "couldn't find FXServer build ${FX_BUILD:-recommended}"
  build=$(basename "$(dirname "$url")" | cut -d- -f1)
  dest=/opt/fivem/artifacts/$build
  if [ ! -x "$dest/run.sh" ]; then
    info "downloading build $build..."
    mkdir -p "$dest.tmp"
    curl -fsSL "$url" | tar -xJ -C "$dest.tmp" || die "download failed"
    rm -rf "$dest"; mv "$dest.tmp" "$dest"
  fi
  local was; was=$(readlink /opt/fivem/current 2>/dev/null || true)
  ln -sfn "$dest" /opt/fivem/current
  if [ -n "$was" ] && [ "$was" != "$dest" ]; then
    ok "build $build (each server switches to it when its service next restarts)"
  else
    ok "build $build"
  fi
}
