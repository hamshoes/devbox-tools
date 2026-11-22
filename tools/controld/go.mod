module github.com/hamshoes/devbox-tools/tools/controld

go 1.27.1

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/hamshoes/devbox-tools/pairs/control => ../../pairs/control

replace github.com/hamshoes/devbox-tools/pkg/platform => ../../pkg/platform

replace github.com/hamshoes/devbox-tools/pkg/tetherapp => ../../pkg/tetherapp

require (
	github.com/hamshoes/devbox-tools/pairs/control v0.0.0
	github.com/hamshoes/devbox-tools/pairs/notify v0.0.0
	github.com/hamshoes/devbox-tools/pkg/platform v0.0.0
	github.com/hamshoes/devbox-tools/pkg/tetherapp v0.0.0
)

replace github.com/hamshoes/devbox-tools/pairs/notify => ../../pairs/notify
