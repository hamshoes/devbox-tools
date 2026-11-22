module github.com/hamshoes/devbox-tools/tools/bigbrother

go 1.27.1

require (
	github.com/hamshoes/devbox-tools/pairs/control v0.0.0
	github.com/hamshoes/devbox-tools/pkg/platform v0.0.0
	github.com/hamshoes/devbox-tools/pkg/tetherapp v0.0.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/hamshoes/devbox-tools/pairs/control => ../../pairs/control

replace github.com/hamshoes/devbox-tools/pkg/platform => ../../pkg/platform

replace github.com/hamshoes/devbox-tools/pkg/tetherapp => ../../pkg/tetherapp
