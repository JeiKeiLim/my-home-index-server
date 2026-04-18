//go:build !darwin

package inspector

import "context"

// readEnv is a no-op on non-darwin platforms. Port-manager targets
// macOS only; this stub exists solely so the package builds on Linux
// CI boxes where some developers may run `go vet ./...`.
func readEnv(_ context.Context, _ int) ([]string, error) {
	return nil, nil
}
