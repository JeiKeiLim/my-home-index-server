// Package config loads runtime configuration for port-manager. It merges
// values from a .env file (via godotenv), from CLI flags, and from
// Options passed by the caller. Missing AUTH_TOKEN / SESSION_SECRET are
// auto-generated and persisted back to .env; the one-time AUTH_TOKEN is
// printed to stdout when generated.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const (
	// DefaultPort is the dashboard listen port when unset.
	DefaultPort = 40000
	// DefaultPortMin / DefaultPortMax bound the scanner's target range.
	// These are neutral examples — users should set PORT_RANGE in .env
	// to match their own router's port-forward rules.
	DefaultPortMin = 40000
	DefaultPortMax = 40500
	// DefaultKillGraceMS is the SIGTERM→SIGKILL escalation delay.
	DefaultKillGraceMS = 3000
	// DefaultPublicHost is advertised in copied URLs.
	DefaultPublicHost = "yourhost.example"
	// DefaultScanner is the scanner selection strategy.
	DefaultScanner = "auto"
	// MinTokenBytes is the minimum allowed bearer-token length.
	MinTokenBytes = 16
	// MinSecretBytes is the minimum allowed session-secret length.
	MinSecretBytes = 16
	// GeneratedTokenBytes is the random-byte count generated when AUTH_TOKEN
	// or SESSION_SECRET are empty. 32 raw bytes → 64 hex characters.
	GeneratedTokenBytes = 32
)

// Config is the validated runtime configuration consumed by the rest of
// the binary. Fields map 1:1 to the environment variables documented in
// .env.example.
type Config struct {
	AuthToken     string
	SessionSecret string
	PublicHost    string
	Port          int
	PortMin       int
	PortMax       int
	KillGraceMS   int
	Scanner       string
	StateDir      string
	// TrustXFF, when true, instructs the rate-limiter and Auth
	// middleware to derive the remote-IP key from the first hop in
	// X-Forwarded-For instead of r.RemoteAddr. ONLY enable behind a
	// trusted reverse proxy (Caddy/nginx) that always rewrites XFF —
	// on a naked HTTP listener this lets a caller spoof the rate-limit
	// bucket key by setting their own XFF header. Default false.
	TrustXFF bool
}

// Options is the explicit override set fed to Load. Zero values mean
// "use the environment or default"; non-zero values override.
type Options struct {
	// EnvFile, if non-empty, overrides the default ".env" path.
	EnvFile string
	// StdOut is where the generated AUTH_TOKEN banner is printed. If nil,
	// os.Stdout is used.
	StdOut io.Writer

	AuthToken     string
	SessionSecret string
	PublicHost    string
	// Port, if non-nil, overrides the resolved listen port — including a
	// non-nil pointer to 0, which means "bind an ephemeral port" (test
	// helpers use this). nil leaves the port up to $PORT and DefaultPort.
	Port        *int
	PortMin     int
	PortMax     int
	KillGraceMS int
	Scanner     string
	StateDir    string
	// TrustXFF mirrors Config.TrustXFF. Either source (--trust-xff
	// flag or TRUST_XFF env) being true enables the feature; default
	// is false. There is no "explicit-disable from CLI" path because
	// the feature is opt-in.
	TrustXFF bool
}

// ErrValidation wraps configuration validation failures.
var ErrValidation = errors.New("config: validation failed")

// BindFlags wires Options fields onto the given flag.FlagSet so callers
// (typically cmd/port-manager/main.go) can populate them from argv. All
// flags default to zero values — Load treats those as "do not override".
// The --port flag, when supplied, sets Options.Port to a non-nil pointer
// (so callers can opt into an explicit ephemeral bind by passing 0).
func BindFlags(fs *flag.FlagSet, opts *Options) {
	fs.StringVar(&opts.EnvFile, "env-file", "", "path to .env file (default \".env\")")
	fs.StringVar(&opts.PublicHost, "public-host", "", "public hostname used in copied URLs")
	fs.Func("port", "dashboard listen port", func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("config: --port: %w", err)
		}
		opts.Port = &n
		return nil
	})
	fs.IntVar(&opts.KillGraceMS, "kill-grace-ms", 0, "SIGTERM→SIGKILL grace in ms")
	fs.StringVar(&opts.Scanner, "scanner", "", "scanner implementation: auto|libproc|lsof")
	fs.StringVar(&opts.StateDir, "state-dir", "", "directory for state.json (default ~/.port-manager)")
	fs.BoolVar(&opts.TrustXFF, "trust-xff", false, "trust X-Forwarded-For first hop as remote IP (only behind a trusted reverse proxy)")
}

// Load reads .env, overlays opts, applies defaults, auto-generates any
// missing AUTH_TOKEN or SESSION_SECRET (persisting them back to .env
// and printing AUTH_TOKEN once), validates the final set, and returns a
// Config. A non-nil error means validation failed.
func Load(opts Options) (*Config, error) {
	envFile := opts.EnvFile
	if envFile == "" {
		envFile = ".env"
	}
	out := opts.StdOut
	if out == nil {
		out = os.Stdout
	}

	// Parse .env into a map rather than exporting to process env — tests
	// share a process and we don't want one test's .env to leak into
	// another. Missing file is tolerated so first-run users and unit
	// tests without a .env both work.
	fileEnv := map[string]string{}
	if _, err := os.Stat(envFile); err == nil {
		m, err := godotenv.Read(envFile)
		if err != nil {
			return nil, fmt.Errorf("config: read %s: %w", envFile, err)
		}
		fileEnv = m
	}
	lookup := func(key string) string {
		if v, ok := fileEnv[key]; ok && v != "" {
			return v
		}
		return os.Getenv(key)
	}
	lookupInt := func(key string, def int) int {
		if v := lookup(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		return def
	}

	port := lookupInt("PORT", DefaultPort)
	if opts.Port != nil {
		port = *opts.Port
	}

	cfg := &Config{
		AuthToken:     firstNonEmpty(opts.AuthToken, lookup("AUTH_TOKEN")),
		SessionSecret: firstNonEmpty(opts.SessionSecret, lookup("SESSION_SECRET")),
		PublicHost:    firstNonEmpty(opts.PublicHost, lookup("PUBLIC_HOST"), DefaultPublicHost),
		Port:          port,
		KillGraceMS:   firstNonZero(opts.KillGraceMS, lookupInt("KILL_GRACE_MS", DefaultKillGraceMS)),
		Scanner:       firstNonEmpty(opts.Scanner, lookup("SCANNER"), DefaultScanner),
		StateDir:      firstNonEmpty(opts.StateDir, lookup("STATE_DIR")),
		TrustXFF:      opts.TrustXFF || parseBool(lookup("TRUST_XFF")),
	}

	// PORT_RANGE in .env is "<min>-<max>"; opts override either side.
	envMin, envMax := parsePortRange(lookup("PORT_RANGE"))
	cfg.PortMin = firstNonZero(opts.PortMin, envMin, DefaultPortMin)
	cfg.PortMax = firstNonZero(opts.PortMax, envMax, DefaultPortMax)

	if cfg.StateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("config: resolve home dir: %w", err)
		}
		cfg.StateDir = filepath.Join(home, ".port-manager")
	}

	generated := map[string]string{}
	if cfg.AuthToken == "" {
		tok, err := generateSecret(GeneratedTokenBytes)
		if err != nil {
			return nil, fmt.Errorf("config: generate AUTH_TOKEN: %w", err)
		}
		cfg.AuthToken = tok
		generated["AUTH_TOKEN"] = tok
	}
	if cfg.SessionSecret == "" {
		sec, err := generateSecret(GeneratedTokenBytes)
		if err != nil {
			return nil, fmt.Errorf("config: generate SESSION_SECRET: %w", err)
		}
		cfg.SessionSecret = sec
		generated["SESSION_SECRET"] = sec
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}

	if len(generated) > 0 {
		if err := persistGenerated(envFile, generated); err != nil {
			return nil, fmt.Errorf("config: persist generated secrets: %w", err)
		}
		if tok, ok := generated["AUTH_TOKEN"]; ok {
			fmt.Fprintf(out, "port-manager: generated AUTH_TOKEN=%s (persisted to %s)\n", tok, envFile)
		}
	}

	return cfg, nil
}

func validate(c *Config) error {
	switch {
	case len(c.AuthToken) < MinTokenBytes:
		return fmt.Errorf("%w: AUTH_TOKEN must be at least %d chars", ErrValidation, MinTokenBytes)
	case len(c.SessionSecret) < MinSecretBytes:
		return fmt.Errorf("%w: SESSION_SECRET must be at least %d chars", ErrValidation, MinSecretBytes)
	case c.PublicHost == "":
		return fmt.Errorf("%w: PUBLIC_HOST must be set", ErrValidation)
	case c.Port < 0 || c.Port > 65535:
		return fmt.Errorf("%w: PORT %d out of range [0,65535]", ErrValidation, c.Port)
	case c.PortMin <= 0 || c.PortMin > 65535:
		return fmt.Errorf("%w: PORT_MIN %d out of range (1,65535]", ErrValidation, c.PortMin)
	case c.PortMax <= 0 || c.PortMax > 65535:
		return fmt.Errorf("%w: PORT_MAX %d out of range (1,65535]", ErrValidation, c.PortMax)
	case c.PortMin >= c.PortMax:
		return fmt.Errorf("%w: PORT_MIN (%d) must be < PORT_MAX (%d)", ErrValidation, c.PortMin, c.PortMax)
	case c.KillGraceMS <= 0:
		return fmt.Errorf("%w: KILL_GRACE_MS must be > 0", ErrValidation)
	case c.Scanner != "auto" && c.Scanner != "libproc" && c.Scanner != "lsof":
		return fmt.Errorf("%w: SCANNER must be auto|libproc|lsof, got %q", ErrValidation, c.Scanner)
	}
	return nil
}

func generateSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// exportPrefix matches a leading `export` keyword (and surrounding
// whitespace) on a .env line; godotenv accepts this syntax, so we must
// recognize it when scanning for keys to replace, otherwise we double-
// write AUTH_TOKEN= every Load.
var exportPrefix = regexp.MustCompile(`^\s*export\s+`)

// persistGenerated rewrites envFile so the provided keys have the given
// values. Unknown keys in the file are preserved; known keys are
// replaced in place. Missing keys are appended at the end. The write
// goes to envFile+".tmp" with fsync, then os.Rename to envFile, then
// the parent directory is fsynced — losing AUTH_TOKEN after the stdout
// banner has scrolled is unrecoverable, so the durability matters.
func persistGenerated(envFile string, kv map[string]string) error {
	var lines []string
	if data, err := os.ReadFile(envFile); err == nil {
		lines = strings.Split(string(data), "\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Strip optional `export ` prefix before locating the `=`. The
		// rewrite preserves the prefix so the file stays valid for any
		// shell that sourced it.
		body := trimmed
		exportLoc := exportPrefix.FindStringIndex(body)
		hasExport := exportLoc != nil
		if hasExport {
			body = body[exportLoc[1]:]
		}
		eq := strings.IndexByte(body, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(body[:eq])
		if val, ok := kv[key]; ok {
			if hasExport {
				lines[i] = "export " + key + "=" + val
			} else {
				lines[i] = key + "=" + val
			}
			seen[key] = true
		}
	}
	for k, v := range kv {
		if !seen[k] {
			lines = append(lines, k+"="+v)
		}
	}

	// Drop a trailing empty line if we introduced one via Split.
	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	return atomicWriteFile(envFile, []byte(content), 0o600)
}

// atomicWriteFile writes data to dst via dst+".tmp", fsyncs the tmp
// file, renames it onto dst, and fsyncs the containing directory so the
// rename metadata is durable across crashes. Returning nil means the
// whole sequence committed; an error leaves the original dst intact.
func atomicWriteFile(dst string, data []byte, perm os.FileMode) (retErr error) {
	dir := filepath.Dir(dst)
	tmp := dst + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	cleanup := func() {
		if retErr != nil {
			_ = os.Remove(tmp)
		}
	}
	defer cleanup()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}

	// Best-effort parent-dir fsync. Some filesystems (or Windows) reject
	// O_RDONLY on a directory; treat the failure as non-fatal because
	// the rename itself already committed the new contents to dst.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func parsePortRange(s string) (int, int) {
	if s == "" {
		return 0, 0
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	hi, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return lo, hi
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseBool accepts the canonical truthy spellings ("1", "true",
// "yes", "on" — case-insensitive) and treats anything else (including
// empty) as false. Strict-but-lenient: enable-only flags should not
// trip on stylistic variation, but unknown values must NOT silently
// enable a security-sensitive feature like TRUST_XFF.
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func firstNonZero(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}
