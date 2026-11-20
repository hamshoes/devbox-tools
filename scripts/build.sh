#!/usr/bin/env bash
# Usage: build.sh <tool> <version> — cross-compile and package one tool into dist/.
set -euo pipefail
tool=${1:?usage: build.sh <tool> <version>}
version=${2:?usage: build.sh <tool> <version>}
cd "$(git rev-parse --show-toplevel)"

TARGETS="linux/arm64"
[ -f "tools/$tool/.release" ] && . "tools/$tool/.release"

root=$PWD
mkdir -p dist
for target in $TARGETS; do
  os=${target%/*}
  arch=${target#*/}
  out=$(mktemp -d)
  (cd "tools/$tool" && GOWORK=off CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out/$tool" .)
  tar -C "$out" -czf "dist/${tool}_${version}_${os}_${arch}.tar.gz" "$tool"
  rm -rf "$out"
done

(cd dist && sha256sum "${tool}_${version}"_*.tar.gz > "${tool}_${version}_checksums.txt")
