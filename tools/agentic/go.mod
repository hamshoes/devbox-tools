module github.com/hamshoes/devbox-tools/tools/agentic

go 1.27.1

require (
	github.com/hamshoes/devbox-tools/pkg/platform v0.0.0
	mvdan.cc/sh/v3 v3.14.1
)

replace github.com/hamshoes/devbox-tools/pkg/platform => ../../pkg/platform
