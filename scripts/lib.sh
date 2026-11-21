# Shared helpers, sourced by plan.sh and release.sh. Expects to run from the repo root.

# dir_of <kind> <name> — module folder: tools/<name> or pkg/<name>.
dir_of() {
  case $1 in
    tool) echo "tools/$2" ;;
    pkg) echo "pkg/$2" ;;
    *) echo "error: unknown kind '$1'" >&2; return 1 ;;
  esac
}

# tag_prefix <kind> <name> — tools/<name> or pkg/<name> (the same as the folder).
tag_prefix() { dir_of "$@"; }

# tool_deps <tool> — folders of the libraries a tool replaces locally (=> ../../pkg/<name>).
tool_deps() {
  sed -nE 's#^.*=>[[:space:]]*\.\./\.\./pkg/([^/[:space:]]+)[[:space:]]*$#pkg/\1#p' "tools/$1/go.mod"
}

# release_paths <kind> <name> — the folders whose changes make a release: its own, plus its libraries'.
release_paths() {
  dir_of "$1" "$2"
  if [ "$1" = tool ]; then tool_deps "$2"; fi
}
