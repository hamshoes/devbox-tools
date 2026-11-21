#!/usr/bin/env bash
# Usage: install.sh [--os darwin|linux] [--arch arm64|amd64] [--dir <path>] <tool> [version]
# Install a tool's release. Defaults to this machine's OS and architecture, into
# ~/.local/share/devtools/bin on Linux (devbox) and ~/.local/bin on macOS.
set -euo pipefail
usage="usage: install.sh [--os darwin|linux] [--arch arm64|amd64] [--dir <path>] <tool> [version]"
repo=hamshoes/devbox-tools

case $(uname -s) in
  Linux) os=linux; dest=$HOME/.local/share/devtools/bin ;;
  Darwin) os=darwin; dest=$HOME/.local/bin ;;
  *) echo "error: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

args=()
while [ $# -gt 0 ]; do
  case $1 in
    --os) os=${2:?$usage}; shift 2 ;;
    --arch) arch=${2:?$usage}; shift 2 ;;
    --dir) dest=${2:?$usage}; shift 2 ;;
    -h|--help) echo "$usage"; exit 0 ;;
    -*) echo "error: unknown option $1" >&2; echo "$usage" >&2; exit 1 ;;
    *) args+=("$1"); shift ;;
  esac
done
case $os in darwin|linux) ;; *) echo "error: --os must be darwin or linux" >&2; exit 1 ;; esac
case $arch in arm64|amd64) ;; *) echo "error: --arch must be arm64 or amd64" >&2; exit 1 ;; esac
[ ${#args[@]} -ge 1 ] && [ ${#args[@]} -le 2 ] || { echo "$usage" >&2; exit 1; }
tool=${args[0]}
version=${args[1]-}

if [ -z "$version" ]; then
  version=$(gh release list -R "$repo" --limit 200 --json tagName -q '.[].tagName' \
    | grep "^tools/$tool/v" | sort -V | tail -1 || true)
  version=${version##*/}
fi
[ -n "$version" ] || { echo "error: no release found for $tool" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tag="tools/$tool/$version"
archive="${tool}_${version}_${os}_${arch}.tar.gz"
sums="${tool}_${version}_checksums.txt"
gh release download "$tag" -R "$repo" -D "$tmp" -p "$archive" -p "$sums"

# sha256sum is GNU; macOS has shasum.
if command -v sha256sum >/dev/null; then actual=$(sha256sum "$tmp/$archive"); else actual=$(shasum -a 256 "$tmp/$archive"); fi
expected=$(grep " ${archive}\$" "$tmp/$sums" | cut -d' ' -f1)
[ -n "$expected" ] && [ "${actual%% *}" = "$expected" ] \
  || { echo "error: checksum mismatch for $archive" >&2; exit 1; }

mkdir -p "$dest"
tar -xzf "$tmp/$archive" -C "$dest"
echo "installed $tool $version ($os/$arch) to $dest"
