module github.com/hamshoes/devbox-tools/tools/tether-gw

go 1.27.1

require (
	github.com/hamshoes/devbox-tools/pkg/tether v0.0.0
	github.com/hamshoes/devbox-tools/pkg/tetherapp v0.0.0
	golang.org/x/sys v0.50.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
)

replace github.com/hamshoes/devbox-tools/pkg/tether => ../../pkg/tether

replace github.com/hamshoes/devbox-tools/pkg/tetherapp => ../../pkg/tetherapp
