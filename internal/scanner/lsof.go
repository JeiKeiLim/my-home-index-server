package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	lsofName = "lsof"

	// scanBudget enforces Iron Law 6 (scan budget ≤ 1s) even when the
	// caller passes a longer deadline. Both scanner implementations
	// wrap their Scan entry point with this timeout.
	scanBudget = 1 * time.Second
)

type lsofScanner struct {
	cfg *Config
}

// NewLsof returns the lsof-shelling scanner. Used as the darwin
// fallback when SCANNER=lsof is forced, and the only option on
// non-darwin hosts (where spec §10 already marks the platform out of
// scope — present for compile parity).
func NewLsof(cfg *Config) (Scanner, error) {
	if cfg == nil {
		return nil, fmt.Errorf("scanner: nil config")
	}
	return &lsofScanner{cfg: cfg}, nil
}

func (s *lsofScanner) Name() string { return lsofName }

// Scan shells out to lsof with the argv from the research note:
//
//	lsof -iTCP -sTCP:LISTEN -P -n -a -u $(id -u) -F pPn -b
//
// The 1s exec.CommandContext timeout caps the whole call. cmd.Output()
// is used exclusively (never StdoutPipe without Wait) so the child is
// always reaped and no pipe fds leak across 500-iteration loops.
//
// Exit-code semantics: lsof exits 1 in a number of benign cases —
// "matched nothing" but also "one of the per-pid probes failed under
// -b" (e.g. a stalled Time Machine mount or ephemeral EPERM on another
// user's process the global table happened to include). In both, the
// non-failing process records are still emitted on stdout. We therefore
// always parse stdout first; we only surface an error when stdout
// produced zero listeners AND exit code is ≥ 2 (a real lsof failure
// like bad argv or missing binary).
func (s *lsofScanner) Scan(ctx context.Context) ([]Listener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, scanBudget)
	defer cancel()

	uid := strconv.Itoa(os.Getuid())
	cmd := exec.CommandContext(callCtx,
		"lsof",
		"-iTCP",
		"-sTCP:LISTEN",
		"-P",
		"-n",
		"-a",
		"-u", uid,
		"-F", "pPn",
		"-b",
	)
	// Discard stderr outright. Stalled APFS/Time Machine mounts emit
	// warnings that would otherwise appear in caller logs; -b already
	// tells lsof to skip blocking syscalls on those mounts.
	cmd.Stderr = io.Discard

	out, err := cmd.Output()
	// If ctx expired mid-run, report that — exec's signal-kill error
	// is less actionable than context.DeadlineExceeded.
	if cerr := callCtx.Err(); cerr != nil {
		return nil, cerr
	}

	listeners := parseLsofF(out, s.cfg.PortMin, s.cfg.PortMax, os.Getpid())

	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code := ee.ExitCode()
			// Exit 1 is benign: either "no match" or partial per-pid
			// probe failures (-b mode). Whatever stdout produced is
			// authoritative.
			if code <= 1 {
				return listeners, nil
			}
			// Exit ≥ 2 is a real failure. Surface it only when stdout
			// also produced nothing — otherwise the partial result is
			// still useful and consistent with the libproc path which
			// silently skips inaccessible pids.
			if len(listeners) == 0 {
				return nil, fmt.Errorf("scanner: lsof exited %d: %w", code, err)
			}
			return listeners, nil
		}
		// Non-ExitError (binary missing, signal kill that ctx didn't
		// catch, etc.). Surface unconditionally.
		return nil, fmt.Errorf("scanner: lsof: %w", err)
	}

	return listeners, nil
}

// parseLsofF parses lsof -F pPn output. Each line starts with a
// single-char tag:
//
//	p<pid>    new process record
//	P<proto>  protocol ("TCP")
//	n<node>   local address ("*:40123", "127.0.0.1:40123", "[::1]:40123")
//
// State is carried across lines: p resets curPID, subsequent P/n lines
// describe that pid's sockets until the next p.
func parseLsofF(data []byte, portMin, portMax, excludePID int) []Listener {
	type key struct{ pid, port int }
	agg := map[key]*Listener{}

	var curPID int
	var curProto string

	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		line := bytes.TrimRight(raw, "\r")
		if len(line) == 0 {
			continue
		}
		tag := line[0]
		body := string(line[1:])
		switch tag {
		case 'p':
			pid, err := strconv.Atoi(body)
			if err != nil {
				curPID = 0
				continue
			}
			curPID = pid
			curProto = ""
		case 'P':
			curProto = body
		case 'n':
			if curPID == 0 || curPID == excludePID {
				continue
			}
			if !strings.EqualFold(curProto, "TCP") {
				continue
			}
			host, port, ok := splitHostPort(body)
			if !ok {
				continue
			}
			if port < portMin || port > portMax {
				continue
			}
			k := key{pid: curPID, port: port}
			l, exists := agg[k]
			if !exists {
				l = &Listener{
					PID:      curPID,
					Port:     port,
					Protocol: "tcp",
					Source:   lsofName,
				}
				agg[k] = l
			}
			l.Addrs = append(l.Addrs, net.JoinHostPort(host, strconv.Itoa(port)))
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
	return out
}

// splitHostPort handles lsof -n's three node shapes:
//
//	"*:40123"            → ("0.0.0.0", 40123)
//	"127.0.0.1:40123"    → ("127.0.0.1", 40123)
//	"[::1]:40123"        → ("::1", 40123)
func splitHostPort(node string) (string, int, bool) {
	if strings.HasPrefix(node, "[") {
		end := strings.LastIndex(node, "]")
		if end < 0 || end+2 > len(node) || node[end+1] != ':' {
			return "", 0, false
		}
		host := node[1:end]
		port, err := strconv.Atoi(node[end+2:])
		if err != nil {
			return "", 0, false
		}
		return host, port, true
	}
	idx := strings.LastIndex(node, ":")
	if idx < 0 {
		return "", 0, false
	}
	host := node[:idx]
	if host == "*" {
		host = "0.0.0.0"
	}
	port, err := strconv.Atoi(node[idx+1:])
	if err != nil {
		return "", 0, false
	}
	return host, port, true
}
