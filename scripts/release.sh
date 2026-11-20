#!/usr/bin/env bash
# Usage: release.sh <tool> <version> <prev> — create the GitHub release for one tool from dist/.
set -euo pipefail
tool=${1:?usage: release.sh <tool> <version> <prev>}
version=${2:?usage: release.sh <tool> <version> <prev>}
prev=${3-}
cd "$(git rev-parse --show-toplevel)"

tag="tools/$tool/$version"
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null || gh release view "$tag" >/dev/null 2>&1; then
  echo "error: tag/release $tag already exists; refusing to overwrite" >&2
  exit 1
fi

if [ -n "$prev" ]; then
  notes=$(git log --format='- %s (%h)' "tools/$tool/$prev..HEAD" -- "tools/$tool")
else
  notes=$(git log --format='- %s (%h)' -- "tools/$tool")
fi

gh release create "$tag" dist/* \
  --title "$tool $version" --notes "$notes" --target "$GITHUB_SHA" --latest=false
