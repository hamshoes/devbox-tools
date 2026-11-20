#!/usr/bin/env bash
# Prints `any=` and `matrix=` lines for $GITHUB_OUTPUT: which tools need a release, and their next versions.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

matrix='[]'
for mod in tools/*/go.mod; do
  [ -e "$mod" ] || continue
  dir=$(dirname "$mod")
  tool=$(basename "$dir")
  prev=$(git tag -l "tools/$tool/v*" --sort=-v:refname | head -n1)

  if [ -z "$prev" ]; then
    version=v0.1.0
  else
    if git diff --quiet "$prev" HEAD -- "$dir" ":(glob,exclude)$dir/**/*.md"; then
      continue
    fi
    bump=patch
    trailers=$(git log --format='%(trailers:key=Release-As,valueonly)' "$prev"..HEAD -- "$dir" | tr '[:upper:]' '[:lower:]' | tr -d ' ')
    if grep -qx major <<<"$trailers"; then
      bump=major
    elif grep -qx minor <<<"$trailers"; then
      bump=minor
    fi
    IFS=. read -r major minor patch <<<"${prev##*/v}"
    case $bump in
      major) version="v$((major + 1)).0.0" ;;
      minor) version="v$major.$((minor + 1)).0" ;;
      patch) version="v$major.$minor.$((patch + 1))" ;;
    esac
  fi

  matrix=$(jq -c --arg tool "$tool" --arg version "$version" --arg prev "${prev:+${prev##*/}}" \
    '. + [{tool: $tool, version: $version, prev: $prev}]' <<<"$matrix")
done

if [ "$matrix" = '[]' ]; then echo "any=false"; else echo "any=true"; fi
echo "matrix=$matrix"
