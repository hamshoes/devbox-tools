scratch() {
  [[ -r $HOME/.config/box/env ]] && source "$HOME/.config/box/env"
  case "$1" in
    ls|clean|init|--version|-h|--help)
      command scratch "$@" ;;
    *)
      [[ " $* " == *" -h "* || " $* " == *" --help "* ]] && { command scratch "$@"; return; }
      local dir
      dir=$(command scratch "$@") || return
      [[ -n $dir && -d $dir ]] && cd -- "$dir" ;;
  esac
}
