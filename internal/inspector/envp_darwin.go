//go:build darwin

package inspector

// KERN_PROCARGS2 returns a flat buffer for a target PID containing:
//
//	argc (int32)
//	exec_path\0 (null-padded to word boundary)
//	argv[0]\0 argv[1]\0 ... argv[argc-1]\0
//	envp[0]\0 envp[1]\0 ... envp[n-1]\0
//
// We skip argc+exec_path+argv and return the env slice. gopsutil's
// Environ() is not implemented on darwin in v4.26.3 (process_bsd.go:63
// returns ErrNotImplementedError), so this helper closes that gap.
//
// Same-uid reads are unprivileged; cross-uid reads return EPERM, which
// we translate to ErrPermission for graceful handling.

/*
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <sys/sysctl.h>
#include <sys/types.h>

// pm_read_procargs fetches KERN_PROCARGS2 for pid into a caller-owned
// buffer and writes the actual size back through outLen. Returns 0 on
// success, otherwise the positive errno value.
static int pm_read_procargs(int pid, void *buf, size_t *buflen) {
    int mib[3] = { CTL_KERN, KERN_PROCARGS2, pid };
    size_t n = *buflen;
    if (sysctl(mib, 3, buf, &n, NULL, 0) != 0) {
        return errno;
    }
    *buflen = n;
    return 0;
}

// pm_argmax returns the kernel's advertised upper bound for a single
// KERN_PROCARGS2 buffer. Typical value is 1 MiB on macOS 26.
static int pm_argmax(size_t *out) {
    int mib[2] = { CTL_KERN, KERN_ARGMAX };
    int argmax = 0;
    size_t n = sizeof(argmax);
    if (sysctl(mib, 2, &argmax, &n, NULL, 0) != 0) {
        return errno;
    }
    if (argmax <= 0) { return EINVAL; }
    *out = (size_t)argmax;
    return 0;
}
*/
import "C"

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// readEnv returns the envp[] of pid using sysctl KERN_PROCARGS2.
//
// Behavior:
//   - returns (nil, nil) if pid has no env (rare but legal)
//   - returns (nil, ErrNotFound) if pid has exited
//   - returns (nil, ErrPermission) on cross-uid EPERM
//   - honours ctx cancellation between the two sysctl calls
func readEnv(ctx context.Context, pid int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("readEnv: %w", err)
	}

	var maxLen C.size_t
	if rc := C.pm_argmax(&maxLen); rc != 0 {
		return nil, fmt.Errorf("readEnv: sysctl KERN_ARGMAX: %w", syscall.Errno(rc))
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("readEnv: %w", err)
	}

	buf := make([]byte, int(maxLen))
	got := C.size_t(len(buf))
	rc := C.pm_read_procargs(C.int(pid), unsafe.Pointer(&buf[0]), &got)
	if rc != 0 {
		errno := syscall.Errno(rc)
		switch errno {
		case syscall.ESRCH:
			return nil, fmt.Errorf("%w: %v", ErrNotFound, errno)
		case syscall.EPERM, syscall.EACCES:
			return nil, fmt.Errorf("%w: %v", ErrPermission, errno)
		case syscall.EINVAL:
			// EINVAL from sysctl KERN_PROCARGS2 is ambiguous: it may
			// mean a bad mib, a bad buflen, or — empirically on
			// macOS 26 — that the kernel refused the read because the
			// pid is either gone or owned by a foreign uid (cross-uid
			// reads on macOS 26 surface as EINVAL rather than EPERM
			// for the procargs path). Probe with kill(pid, 0) to
			// disambiguate without leaking the raw errno: ESRCH means
			// the pid is gone, EPERM means cross-uid. Anything else is
			// a real programming error and we surface it verbatim.
			if perr := syscall.Kill(pid, 0); perr != nil {
				switch {
				case errors.Is(perr, syscall.ESRCH):
					return nil, fmt.Errorf("%w: pid %d (sysctl EINVAL, kill ESRCH)", ErrNotFound, pid)
				case errors.Is(perr, syscall.EPERM):
					return nil, fmt.Errorf("%w: pid %d (sysctl EINVAL, kill EPERM)", ErrPermission, pid)
				}
			}
			return nil, fmt.Errorf("readEnv: sysctl KERN_PROCARGS2 EINVAL (programming error or kernel rejection): %w", errno)
		default:
			return nil, fmt.Errorf("readEnv: sysctl KERN_PROCARGS2: %w", errno)
		}
	}
	return parseProcargs(buf[:int(got)])
}

// parseProcargs walks the KERN_PROCARGS2 buffer and returns its
// envp[]. The layout is:
//
//	[0..4)                    argc (native-endian int32)
//	[4..4+path_len]           exec_path, NUL-terminated, then NUL
//	                          padding to the next non-NUL byte
//	argv[0..argc)             NUL-terminated strings
//	envp[0..first empty)      NUL-terminated strings
//
// Behaviour is defensive: every length check is explicit so a
// truncated buffer can't index out of bounds. If argc is absurd (>10k)
// we return an error because it's almost certainly a buffer read gone
// wrong, not a real process.
func parseProcargs(buf []byte) ([]string, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("readEnv: short buffer (%d bytes)", len(buf))
	}
	argc := int(int32(binary.LittleEndian.Uint32(buf[:4])))
	if argc < 0 || argc > 10000 {
		return nil, fmt.Errorf("readEnv: implausible argc %d", argc)
	}

	// Step over the exec_path (a NUL-terminated string followed by
	// NUL padding to the first non-NUL byte).
	pos := 4
	for pos < len(buf) && buf[pos] != 0 {
		pos++
	}
	for pos < len(buf) && buf[pos] == 0 {
		pos++
	}

	// Skip argc argv entries.
	for i := 0; i < argc; i++ {
		for pos < len(buf) && buf[pos] != 0 {
			pos++
		}
		if pos >= len(buf) {
			// argv ran off the end; nothing to return.
			return nil, nil
		}
		pos++ // step past NUL
	}

	// Collect env entries until we hit an empty string (end marker)
	// or run out of buffer.
	var env []string
	for pos < len(buf) {
		start := pos
		for pos < len(buf) && buf[pos] != 0 {
			pos++
		}
		if pos == start {
			break
		}
		env = append(env, string(buf[start:pos]))
		if pos < len(buf) {
			pos++
		}
	}
	return env, nil
}
