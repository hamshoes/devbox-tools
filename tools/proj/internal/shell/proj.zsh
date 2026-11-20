proj() {
  [[ -r $HOME/.config/devbox/env ]] && source "$HOME/.config/devbox/env"
  case "$1 $2" in
    "scratch ls"*|"scratch clean"*|init*|--version*|"")
      command proj "$@" ;;
    start*|work*|scratch*)
      local dir
      dir=$(command proj "$@") || return
      [[ -n $dir && -d $dir ]] && cd -- "$dir" ;;
    *)
      command proj "$@" ;;
  esac
}
scratch() { proj scratch "$@"; }
