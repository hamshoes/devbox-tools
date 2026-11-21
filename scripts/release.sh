#!/usr/bin/env bash
# Usage: release.sh <kind> <name> <version> <prev> — create the GitHub release for one tool or pair (from dist/) or library (tag and notes only).
set -euo pipefail
kind=${1:?usage: release.sh <tool|pair|pkg> <name> <version> <prev>}
name=${2:?usage: release.sh <tool|pair|pkg> <name> <version> <prev>}
version=${3:?usage: release.sh <tool|pair|pkg> <name> <version> <prev>}
prev=${4-}
cd "$(git rev-parse --show-toplevel)"
. scripts/lib.sh

prefix=$(tag_prefix "$kind" "$name")
tag="$prefix/$version"
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null || gh release view "$tag" >/dev/null 2>&1; then
  echo "error: tag/release $tag already exists; refusing to overwrite" >&2
  exit 1
fi

paths=$(release_paths "$kind" "$name")
range=()
[ -n "$prev" ] && range=("$prefix/$prev..HEAD")
notes=$(git log --format='- %s (%h)' ${range[@]+"${range[@]}"} -- $paths)

assets=()
[ "$kind" != pkg ] && assets=(dist/*)

gh release create "$tag" ${assets[@]+"${assets[@]}"} \
  --title "$name $version" --notes "$notes" --target "$GITHUB_SHA" --latest=false
