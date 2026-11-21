#!/usr/bin/env bash
# Usage: pair-generate.sh <name> — regenerate pairs/<name>/gen from its protobuf schema (pinned buf and protoc-gen-go).
set -euo pipefail
name=${1:?usage: pair-generate.sh <name>}
cd "$(git rev-parse --show-toplevel)"
. scripts/lib.sh

bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
GOBIN=$bin GOWORK=off go install "google.golang.org/protobuf/cmd/protoc-gen-go@$PROTOC_GEN_GO_VERSION"
(cd "pairs/$name" && PATH=$bin:$PATH GOWORK=off go run "github.com/bufbuild/buf/cmd/buf@$BUF_VERSION" generate)
