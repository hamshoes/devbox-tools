// Package shell holds the shell integration scripts.
package shell

import _ "embed"

//go:embed scratch.zsh
var Zsh string
