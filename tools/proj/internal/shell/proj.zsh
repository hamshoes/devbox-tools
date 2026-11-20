proj() {
  [[ -r $HOME/.config/devbox/env ]] && source "$HOME/.config/devbox/env"
  case "$1 $2" in
    start*|work*)
      [[ " $* " == *" -h "* || " $* " == *" --help "* ]] && { command proj "$@"; return; }
      local dir
      dir=$(command proj "$@") || return
      [[ -n $dir && -d $dir ]] && cd -- "$dir" ;;
    *)
      command proj "$@" ;;
  esac
}
