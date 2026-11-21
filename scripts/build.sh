#!/usr/bin/env bash
# Usage: build.sh <kind> <name> <version> — cross-compile and package one tool into dist/. Libraries have nothing to package.
set -euo pipefail
kind=${1:?usage: build.sh <tool|pkg> <name> <version>}
name=${2:?usage: build.sh <tool|pkg> <name> <version>}
version=${3:?usage: build.sh <tool|pkg> <name> <version>}
cd "$(git rev-parse --show-toplevel)"

if [ "$kind" = pkg ]; then
  echo "pkg/$name $version: library, nothing to package"
  exit 0
fi
tool=$name

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
