#!/usr/bin/env bash
# Usage: build.sh <kind> <name> <version> — cross-compile and package one tool or pair into dist/. Libraries have nothing to package.
set -euo pipefail
kind=${1:?usage: build.sh <tool|pair|pkg> <name> <version>}
name=${2:?usage: build.sh <tool|pair|pkg> <name> <version>}
version=${3:?usage: build.sh <tool|pair|pkg> <name> <version>}
cd "$(git rev-parse --show-toplevel)"
. scripts/lib.sh

if [ "$kind" = pkg ]; then
  echo "pkg/$name $version: library, nothing to package"
  exit 0
fi

root=$PWD
mkdir -p dist

if [ "$kind" = pair ]; then
  dir=pairs/$name
  stage=$(mktemp -d)
  (cd "$dir" && GOWORK=off go run "github.com/bufbuild/buf/cmd/buf@$BUF_VERSION" build proto -o "$stage/api.binpb")
  sha=$(sha256sum "$stage/api.binpb" | cut -d' ' -f1)
  jq --arg v "$version" --arg sha "$sha" '. + {version: $v, schema: {sha256: $sha}}' "$dir/manifest.json" > "$stage/manifest.json"

  for side in vm mac; do
    out=$(mktemp -d)
    cp "$stage/manifest.json" "$stage/api.binpb" "$out/"
    app=$(jq -r --arg s "$side" '.[$s].app // empty' "$dir/manifest.json")
    if [ -d "$dir/$side" ]; then
      [ -n "$app" ] || { echo "error: $dir/$side exists but manifest.json has no $side.app" >&2; exit 1; }
      case $side in vm) goos=linux goarch=amd64 ;; mac) goos=darwin goarch=arm64 ;; esac
      (cd "$dir" && GOWORK=off CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
        go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out/$app" "./$side")
    elif [ -n "$app" ]; then
      echo "error: manifest.json names $side.app '$app' but $dir/$side does not exist" >&2
      exit 1
    fi
    tar -C "$out" -czf "dist/pair-${name}_${version}_${side}.tar.gz" $(cd "$out" && ls)
    rm -rf "$out"
  done
  rm -rf "$stage"
  (cd dist && sha256sum "pair-${name}_${version}"_*.tar.gz > "pair-${name}_${version}_checksums.txt")
  exit 0
fi

tool=$name

TARGETS="linux/amd64"
[ -f "tools/$tool/.release" ] && . "tools/$tool/.release"

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
