module github.com/hamshoes/devbox-tools/pairs/open

go 1.27.1

require (
	github.com/hamshoes/devbox-tools/pairs/notify v0.0.0
	github.com/hamshoes/devbox-tools/pkg/tetherapp v0.0.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/hamshoes/devbox-tools/pairs/notify => ../../pairs/notify

replace github.com/hamshoes/devbox-tools/pkg/tetherapp => ../../pkg/tetherapp

replace github.com/hamshoes/devbox-tools/pkg/platform => ../../pkg/platform
