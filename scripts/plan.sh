#!/usr/bin/env bash
# Prints `any=` and `matrix=` lines for $GITHUB_OUTPUT: which tools, pairs and libraries need a release, and their next versions.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
. scripts/lib.sh

jq -e '.version == 1 and (.tools | type == "array")' manifest.json >/dev/null 2>&1 \
  || { echo "error: manifest.json is missing, not valid JSON, or its version is not 1" >&2; exit 1; }
seen=
while IFS=$'\t' read -r name kind; do
  case $kind in
    tool) [ -f "tools/$name/go.mod" ] || { echo "error: manifest.json lists tool '$name' but tools/$name/go.mod does not exist" >&2; exit 1; } ;;
    pair) { [ -f "pairs/$name/go.mod" ] && [ -f "pairs/$name/manifest.json" ]; } || { echo "error: manifest.json lists pair '$name' but pairs/$name/go.mod or manifest.json does not exist" >&2; exit 1; } ;;
    *) echo "error: manifest.json lists '$name' with unknown kind '$kind'" >&2; exit 1 ;;
  esac
  case " $seen " in
    *" $kind/$name "*) echo "error: manifest.json lists $kind '$name' more than once" >&2; exit 1 ;;
  esac
  seen="$seen $kind/$name"
done < <(jq -r '.tools[] | [.name, (.kind // "tool")] | @tsv' manifest.json)

matrix='[]'
for mod in tools/*/go.mod pkg/*/go.mod pairs/*/go.mod; do
  [ -e "$mod" ] || continue
  dir=$(dirname "$mod")
  name=$(basename "$dir")
  case $dir in tools/*) kind=tool ;; pairs/*) kind=pair ;; *) kind=pkg ;; esac

  paths=$(release_paths "$kind" "$name")
  specs=()
  for p in $paths; do
    [ -f "$p/go.mod" ] || { echo "error: $dir replaces $p, which has no go.mod" >&2; exit 1; }
    specs+=("$p" ":(glob,exclude)$p/**/*.md")
  done

  prefix=$(tag_prefix "$kind" "$name")
  prev=$(git tag -l "$prefix/v*" --sort=-v:refname | head -n1)

  if [ -z "$prev" ]; then
    version=v0.1.0
  else
    if git diff --quiet "$prev" HEAD -- "${specs[@]}"; then
      continue
    fi
    bump=patch
    trailers=$(git log --format='%(trailers:key=Release-As,valueonly)' "$prev"..HEAD -- "${specs[@]}" | tr '[:upper:]' '[:lower:]' | tr -d ' ')
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

  matrix=$(jq -c --arg kind "$kind" --arg name "$name" --arg dir "$dir" --arg version "$version" --arg prev "${prev:+${prev##*/}}" \
    '. + [{kind: $kind, name: $name, dir: $dir, version: $version, prev: $prev}]' <<<"$matrix")
done

if [ "$matrix" = '[]' ]; then echo "any=false"; else echo "any=true"; fi
echo "matrix=$matrix"
