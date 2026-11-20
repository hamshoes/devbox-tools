#!/usr/bin/env bash
# Prints `any=` and `matrix=` lines for $GITHUB_OUTPUT: which tools need a release, and their next versions.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

jq -e '.version == 1 and (.tools | type == "array")' manifest.json >/dev/null 2>&1 \
  || { echo "error: manifest.json is missing, not valid JSON, or its version is not 1" >&2; exit 1; }
seen=
for name in $(jq -r '.tools[].name' manifest.json); do
  [ -f "tools/$name/go.mod" ] || { echo "error: manifest.json lists '$name' but tools/$name/go.mod does not exist" >&2; exit 1; }
  case " $seen " in
    *" $name "*) echo "error: manifest.json lists '$name' more than once" >&2; exit 1 ;;
  esac
  seen="$seen $name"
done

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
