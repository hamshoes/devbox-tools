# Shared helpers, sourced by plan.sh and release.sh. Expects to run from the repo root.

# Pinned toolchain for pairs' schemas and generated code.
BUF_VERSION=v1.75.0
PROTOC_GEN_GO_VERSION=v1.36.11

# dir_of <kind> <name> — module folder: tools/<name>, pkg/<name> or pairs/<name>.
dir_of() {
  case $1 in
    tool) echo "tools/$2" ;;
    pkg) echo "pkg/$2" ;;
    pair) echo "pairs/$2" ;;
    *) echo "error: unknown kind '$1'" >&2; return 1 ;;
  esac
}

# tag_prefix <kind> <name> — tools/<name>, pkg/<name> or pairs/<name> (the same as the folder).
tag_prefix() { dir_of "$@"; }

# mod_deps <kind> <name> — folders of the libraries and pairs a module replaces locally (=> ../../pkg/<name>, ../../pairs/<name>).
mod_deps() {
  sed -nE 's#^.*=>[[:space:]]*\.\./\.\./(pkg|pairs)/([^/[:space:]]+)[[:space:]]*$#\1/\2#p' "$(dir_of "$1" "$2")/go.mod"
}

# release_paths <kind> <name> — the folders whose changes make a release: its own, plus its dependencies'.
release_paths() {
  dir_of "$1" "$2"
  if [ "$1" != pkg ]; then mod_deps "$1" "$2"; fi
}
