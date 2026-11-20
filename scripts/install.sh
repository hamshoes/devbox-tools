#!/usr/bin/env bash
# Usage: install.sh <tool> [version] — install a tool's release into devbox.
set -euo pipefail
tool=${1:?usage: install.sh <tool> [version]}
version=${2-}
repo=hamshoes/devbox-tools
dest=$HOME/.local/share/devtools/bin

if [ -z "$version" ]; then
  version=$(gh release list -R "$repo" --limit 200 --json tagName -q '.[].tagName' \
    | grep "^tools/$tool/v" | sort -V | tail -1 || true)
  version=${version##*/}
fi
[ -n "$version" ] || { echo "error: no release found for $tool" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tag="tools/$tool/$version"
gh release download "$tag" -R "$repo" -D "$tmp" \
  -p "*_linux_arm64.tar.gz" -p "${tool}_${version}_checksums.txt"
(cd "$tmp" && sha256sum -c --ignore-missing "${tool}_${version}_checksums.txt")

mkdir -p "$dest"
tar -xzf "$tmp"/*_linux_arm64.tar.gz -C "$dest"
echo "installed $tool $version"
