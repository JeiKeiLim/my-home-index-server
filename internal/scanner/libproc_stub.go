//go:build !darwin

package scanner

import "fmt"

const defaultScannerName = "lsof"

// NewLibproc is darwin-only. On other platforms the symbol exists so
// Auto("libproc") compiles and returns a clean error rather than a
// link-time failure.
func NewLibproc(cfg *Config) (Scanner, error) {
	return nil, fmt.Errorf("scanner: libproc requires darwin")
}
