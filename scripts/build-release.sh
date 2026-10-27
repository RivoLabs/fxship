#!/usr/bin/env bash
# Builds the release files into dist/:
#
#   fxship_<version>_linux_amd64.tar.gz    install.sh, the installer, bin/fxship, examples
#   fxship_<version>_windows_amd64.zip     install.ps1, fxship.exe, luac.exe, examples
#   install.sh, install.ps1                the one-line installers (they fetch a bundle)
#   checksums.txt                          sha256 of each, checked by the installers and upgrade
#
#   scripts/build-release.sh v1.2.3
#
# Needs Go, zip, and x86_64-w64-mingw32-gcc for luac.exe (apt install gcc-mingw-w64-x86-64).
set -euo pipefail
version=${1:?usage: scripts/build-release.sh <version, e.g. v1.2.3>}
ver=${version#v}
root=$(cd "$(dirname "$0")/.." && pwd)
dist=$root/dist
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
rm -rf "$dist"
mkdir -p "$dist"

build() { # build GOOS OUT
  (cd "$root/agent" && CGO_ENABLED=0 GOOS=$1 GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$2" .)
}

# luac.exe for syntax checks on Windows: Lua 5.4, the version FXServer's CfxLua is based on.
lua=5.4.7 lua_sha=9fbf5e28ef86c69858f6d3d34eccc32e911c1a28b4120ff3e84aaa70cfbf1e30
curl -fsSL -o "$work/lua.tgz" "https://www.lua.org/ftp/lua-$lua.tar.gz"
echo "$lua_sha  $work/lua.tgz" | sha256sum -c --quiet
tar -xzf "$work/lua.tgz" -C "$work"
srcs=()
for f in "$work/lua-$lua/src"/*.c; do [ "${f##*/}" = lua.c ] || srcs+=("$f"); done # lua.c is the interpreter
x86_64-w64-mingw32-gcc -O2 -std=gnu99 -static -s -o "$work/luac.exe" "${srcs[@]}" -lm

common() { # common DIR
  cp "$root/LICENSE" "$root/README.md" "$1/"
  cp -r "$root/examples" "$1/"
}

lin=fxship_${ver}_linux_amd64
mkdir -p "$work/$lin/bin"
build linux "$work/$lin/bin/fxship"
cp "$root/install.sh" "$work/$lin/"
cp -r "$root/installer" "$work/$lin/"
common "$work/$lin"
tar -czf "$dist/$lin.tar.gz" -C "$work" --owner=0 --group=0 "$lin"

win=fxship_${ver}_windows_amd64
mkdir -p "$work/$win"
build windows "$work/$win/fxship.exe"
cp "$work/luac.exe" "$root/install.ps1" "$work/$win/"
common "$work/$win"
(cd "$work" && zip -qr "$dist/$win.zip" "$win")

cp "$root/install.sh" "$root/install.ps1" "$dist/"
(cd "$dist" && sha256sum ./*.tar.gz ./*.zip install.sh install.ps1 | sed 's#  \./#  #' >checksums.txt)
ls -l "$dist"
