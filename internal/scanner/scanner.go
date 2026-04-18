// Package scanner enumerates TCP listeners owned by the current user
// within the configured port range. Two implementations exist:
//
//   - libproc (darwin, cgo): walks proc_listallpids → proc_pidinfo →
//     proc_pidfdinfo. Never spawns a subprocess; short-circuits per-pid
//     on UID mismatch to avoid EPERM on other users' processes.
//   - lsof: shells out to lsof with a ≤1s timeout and parses the
//     machine-readable -F output.
//
// Both implementations:
//   - Exclude the current process (os.Getpid()).
//   - Return only TCP listeners whose port is in [cfg.PortMin, cfg.PortMax].
//   - Respect ctx cancellation (Iron Law 6: scan budget ≤ 1s).
//   - Never block the UI: callers set a hard deadline on ctx.
package scanner

import (
	"context"
	"fmt"

	"github.com/JeiKeiLim/my-home-index-server/internal/config"
)

// Config is re-exported so callers don't need a second import just to
// type a parameter.
type Config = config.Config

// Listener describes a TCP LISTEN socket observed by a scanner. A
// single (pid, port) pair may bind multiple addresses (e.g. IPv4 +
// IPv6 dual-stack); those are aggregated into Addrs.
type Listener struct {
	PID      int
	Port     int
	Protocol string   // always "tcp" — UDP is out of scope per spec §10
	Addrs    []string // e.g. ["0.0.0.0:40123", "[::1]:40123"]
	Source   string   // "libproc" | "lsof"
}

// Scanner is the common interface implemented by libproc and lsof.
type Scanner interface {
	Scan(ctx context.Context) ([]Listener, error)
	Name() string
}

// Auto returns the scanner selected by cfg.Scanner:
//
//   - "" or "auto" — libproc on darwin, lsof elsewhere (currently darwin-only)
//   - "libproc"   — the cgo walk (darwin only)
//   - "lsof"      — the subprocess fallback
//
// Anything else is a validation error.
func Auto(cfg *Config) (Scanner, error) {
	if cfg == nil {
		return nil, fmt.Errorf("scanner: nil config")
	}
	name := cfg.Scanner
	if name == "" || name == "auto" {
		name = defaultScannerName
	}
	switch name {
	case "libproc":
		return NewLibproc(cfg)
	case "lsof":
		return NewLsof(cfg)
	default:
		return nil, fmt.Errorf("scanner: unknown scanner %q", name)
	}
}
