module github.com/hamshoes/devbox-tools/tools/dispatcher

go 1.27.1

require (
	github.com/coder/websocket v1.8.15 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/hamshoes/devbox-tools/pkg/tether => ../../pkg/tether

replace github.com/hamshoes/devbox-tools/pkg/dispatch => ../../pkg/dispatch

replace github.com/hamshoes/devbox-tools/pairs/control => ../../pairs/control

replace github.com/hamshoes/devbox-tools/pkg/tetherapp => ../../pkg/tetherapp

replace github.com/hamshoes/devbox-tools/pkg/platform => ../../pkg/platform

require (
	github.com/hamshoes/devbox-tools/pairs/control v0.0.0
	github.com/hamshoes/devbox-tools/pkg/dispatch v0.0.0
	github.com/hamshoes/devbox-tools/pkg/platform v0.0.0
	github.com/hamshoes/devbox-tools/pkg/tether v0.0.0
	github.com/hamshoes/devbox-tools/pkg/tetherapp v0.0.0
)
