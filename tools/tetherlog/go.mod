module github.com/hamshoes/devbox-tools/tools/tetherlog

go 1.27.1

require github.com/hamshoes/devbox-tools/pkg/tether v0.0.0

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
)

replace github.com/hamshoes/devbox-tools/pkg/tether => ../../pkg/tether
