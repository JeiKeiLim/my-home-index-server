//go:build tools
// +build tools

// Package tools pins developer toolchain versions so they travel with
// the module. See harness §Formatting & Linting — golangci-lint is
// pinned at v1.62.2.
//
// Install with:
//
//	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.62.2
//
// The blank import below is guarded by the `tools` build tag so
// `go build ./...` does not attempt to compile it; the pinned version
// is the single source of truth for what the Makefile installs.
package tools

import (
	_ "github.com/golangci/golangci-lint/cmd/golangci-lint" // v1.62.2
)
