//go:build darwin

package scanner

// The cgo walk implemented here is the primary path chosen in
// .tenet/knowledge/2026-04-17_research-macos-tcp-listener-enumeration.md:
// proc_listallpids → proc_pidinfo(PROC_PIDTBSDINFO) → uid filter →
// proc_pidinfo(PROC_PIDLISTFDS) → proc_pidfdinfo(PROC_PIDFDSOCKETINFO).
//
// Helper C functions live in the cgo preamble so the unions inside
// struct socket_info (soi_proto) don't need to be unpacked in Go.

/*
#include <errno.h>
#include <stdint.h>
#include <string.h>
#include <unistd.h>
#include <libproc.h>
#include <sys/proc_info.h>
#include <netinet/in.h>
#include <arpa/inet.h>

// pm_tcp_listener is the flat payload the Go side reads. Unions in
// struct socket_info are opaque to cgo, so we pre-parse here.
typedef struct pm_tcp_listener {
    int      valid;         // 1 = fd is a TCP LISTEN socket
    uint16_t port;          // host byte order
    int      is_ipv6;       // 1 if IPv6-only binding (INI_IPV6 without INI_IPV4)
    char     laddr[46];     // printable local address (INET6_ADDRSTRLEN = 46)
} pm_tcp_listener;

static int pm_list_all_pids(void *buf, int bufsize) {
    return proc_listallpids(buf, bufsize);
}

static int pm_get_bsd_uid(int pid, uint32_t *uid_out) {
    struct proc_bsdinfo info;
    int n = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
    if (n < (int)sizeof(info)) {
        return -1;
    }
    *uid_out = info.pbi_uid;
    return 0;
}

static int pm_list_fds(int pid, void *buf, int bufsize) {
    return proc_pidinfo(pid, PROC_PIDLISTFDS, 0, buf, bufsize);
}

// pm_inspect_fd tests fd on pid. If it's a TCP LISTEN socket, fills
// *out and returns 1. Otherwise sets out->valid=0 and returns 0. A
// negative return indicates proc_pidfdinfo failed (fd closed between
// listing and inspection, EPERM, etc.) and should be ignored silently.
static int pm_inspect_fd(int pid, int fd, pm_tcp_listener *out) {
    struct socket_fdinfo si;
    memset(&si, 0, sizeof(si));
    out->valid = 0;
    out->port = 0;
    out->is_ipv6 = 0;
    out->laddr[0] = 0;
    int n = proc_pidfdinfo(pid, fd, PROC_PIDFDSOCKETINFO, &si, sizeof(si));
    if (n < (int)sizeof(si)) {
        return 0;
    }
    if (si.psi.soi_kind != SOCKINFO_TCP) {
        return 0;
    }
    if (si.psi.soi_proto.pri_tcp.tcpsi_state != TSI_S_LISTEN) {
        return 0;
    }
    out->valid = 1;
    out->port = ntohs((uint16_t)si.psi.soi_proto.pri_tcp.tcpsi_ini.insi_lport);
    uint8_t vflag = si.psi.soi_proto.pri_tcp.tcpsi_ini.insi_vflag;
    if ((vflag & INI_IPV6) && !(vflag & INI_IPV4)) {
        out->is_ipv6 = 1;
        inet_ntop(AF_INET6,
                  &si.psi.soi_proto.pri_tcp.tcpsi_ini.insi_laddr.ina_6,
                  out->laddr, sizeof(out->laddr));
    } else {
        struct in_addr a;
        a.s_addr = si.psi.soi_proto.pri_tcp.tcpsi_ini.insi_laddr.ina_46.i46a_addr4.s_addr;
        inet_ntop(AF_INET, &a, out->laddr, sizeof(out->laddr));
    }
    return 1;
}
*/
import "C"

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"unsafe"
)

const (
	libprocName        = "libproc"
	defaultScannerName = "libproc"

	// proxFdTypeSocket mirrors PROX_FDTYPE_SOCKET from sys/proc_info.h.
	// Declared here as a plain Go const so comparisons against the
	// uint32 proc_fdtype field don't need a C cast on every row.
	proxFdTypeSocket = 2
)

type libprocScanner struct {
	cfg *Config
}

// NewLibproc returns the cgo libproc scanner. It never opens a
// subprocess or long-lived fd; the short-lived syscalls are closed by
// the kernel before proc_pidinfo returns.
func NewLibproc(cfg *Config) (Scanner, error) {
	if cfg == nil {
		return nil, fmt.Errorf("scanner: nil config")
	}
	return &libprocScanner{cfg: cfg}, nil
}

func (s *libprocScanner) Name() string { return libprocName }

// Scan enumerates TCP LISTEN sockets visible to the current uid within
// [cfg.PortMin, cfg.PortMax]. The current process's pid is always
// excluded. ctx is honored between pids (the kernel calls themselves
// do not block, so preemption granularity is "one pid's fd table").
//
// Iron Law 6 (scan budget ≤ 1s) is enforced here even when the caller
// passes a longer deadline: we always derive a child ctx capped at
// scanBudget. The lsof path applies the same cap.
func (s *libprocScanner) Scan(ctx context.Context) ([]Listener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, scanBudget)
	defer cancel()

	pids, err := listAllPIDs()
	if err != nil {
		return nil, err
	}

	ownUID := uint32(os.Getuid())
	ownPID := os.Getpid()
	portMin := s.cfg.PortMin
	portMax := s.cfg.PortMax

	// Aggregate addrs for dual-stack sockets sharing (pid, port).
	type key struct{ pid, port int }
	agg := map[key]*Listener{}

	fdinfoSize := int(unsafe.Sizeof(C.struct_proc_fdinfo{}))

	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if pid <= 0 || int(pid) == ownPID {
			continue
		}

		var uid C.uint32_t
		if C.pm_get_bsd_uid(C.int(pid), &uid) != 0 {
			continue // process gone between listallpids and pidinfo, or EPERM
		}
		if uint32(uid) != ownUID {
			continue
		}

		// Ask the kernel how many bytes the fd table needs.
		bufBytes := int(C.pm_list_fds(C.int(pid), nil, 0))
		if bufBytes <= 0 {
			continue
		}
		// Pad for races: new fds may open between size query and fill.
		slotCount := bufBytes/fdinfoSize + 16
		fdBuf := make([]C.struct_proc_fdinfo, slotCount)
		n := int(C.pm_list_fds(C.int(pid), unsafe.Pointer(&fdBuf[0]), C.int(slotCount*fdinfoSize)))
		if n <= 0 {
			continue
		}
		nfds := n / fdinfoSize
		if nfds > slotCount {
			nfds = slotCount
		}

		for i := 0; i < nfds; i++ {
			fdi := fdBuf[i]
			if uint32(fdi.proc_fdtype) != proxFdTypeSocket {
				continue
			}
			var info C.pm_tcp_listener
			if C.pm_inspect_fd(C.int(pid), C.int(fdi.proc_fd), &info) <= 0 {
				continue
			}
			if info.valid == 0 {
				continue
			}
			port := int(info.port)
			if port < portMin || port > portMax {
				continue
			}
			laddr := C.GoString(&info.laddr[0])
			if laddr == "" {
				laddr = "0.0.0.0"
			}
			hostport := net.JoinHostPort(laddr, strconv.Itoa(port))

			k := key{pid: int(pid), port: port}
			l, ok := agg[k]
			if !ok {
				l = &Listener{
					PID:      int(pid),
					Port:     port,
					Protocol: "tcp",
					Source:   libprocName,
				}
				agg[k] = l
			}
			l.Addrs = append(l.Addrs, hostport)
		}
	}

	out := make([]Listener, 0, len(agg))
	for _, l := range agg {
		sort.Strings(l.Addrs)
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].PID < out[j].PID
	})
	return out, nil
}

// listAllPIDs returns every pid the kernel knows about. Size discovery
// is done with a nil buffer; the fill call is sized with slack so new
// pids spawned between the two calls don't truncate the result.
//
// proc_listallpids return-value convention on darwin: the libproc
// wrapper returns the **count of pids** written (or the pid count the
// kernel would have written if buffer is NULL), NOT the byte count.
// Empirically verified on macOS 26: passing slots=N where N is smaller
// than the real pid count returns exactly N; passing a large buffer
// returns ~798 on a 794-process system. Treating the return as bytes
// and dividing by sizeof(pid_t) silently truncates the pid table to
// ~1/4 and causes the scanner to miss most listeners.
func listAllPIDs() ([]int32, error) {
	count := int(C.pm_list_all_pids(nil, 0))
	if count <= 0 {
		return nil, fmt.Errorf("scanner: proc_listallpids(size) returned %d", count)
	}
	// Pad for races: new pids may spawn between the size query and the
	// fill call. 128 extra slots also absorbs the common case where the
	// size query is a lower bound.
	slots := count + 128
	buf := make([]int32, slots)
	n := int(C.pm_list_all_pids(unsafe.Pointer(&buf[0]), C.int(slots*4)))
	if n <= 0 {
		return nil, fmt.Errorf("scanner: proc_listallpids returned %d", n)
	}
	if n > slots {
		n = slots
	}
	return buf[:n], nil
}
