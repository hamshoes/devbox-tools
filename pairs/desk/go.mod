module github.com/hamshoes/devbox-tools/pairs/desk

go 1.27.1

replace github.com/hamshoes/devbox-tools/pkg/tetherapp => ../../pkg/tetherapp

require (
	github.com/hamshoes/devbox-tools/pkg/platform v0.0.0
	github.com/hamshoes/devbox-tools/pkg/tetherapp v0.0.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/hamshoes/devbox-tools/pkg/platform => ../../pkg/platform
