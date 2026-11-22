module github.com/hamshoes/devbox-tools/tools/bigbrother

go 1.27.1

replace github.com/hamshoes/devbox-tools/pairs/control => ../../pairs/control

replace github.com/hamshoes/devbox-tools/pkg/platform => ../../pkg/platform

replace github.com/hamshoes/devbox-tools/pkg/tetherapp => ../../pkg/tetherapp

require (
	github.com/hamshoes/devbox-tools/pairs/clip v0.0.0
	github.com/hamshoes/devbox-tools/pairs/control v0.0.0
	github.com/hamshoes/devbox-tools/pairs/notify v0.0.0
	github.com/hamshoes/devbox-tools/pairs/open v0.0.0
	github.com/hamshoes/devbox-tools/pairs/send v0.0.0
	github.com/hamshoes/devbox-tools/pkg/platform v0.0.0
	github.com/hamshoes/devbox-tools/pkg/tetherapp v0.0.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/hamshoes/devbox-tools/pairs/clip => ../../pairs/clip

replace github.com/hamshoes/devbox-tools/pairs/notify => ../../pairs/notify

replace github.com/hamshoes/devbox-tools/pairs/open => ../../pairs/open

replace github.com/hamshoes/devbox-tools/pairs/send => ../../pairs/send
