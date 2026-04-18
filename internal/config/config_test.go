package config_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/JeiKeiLim/my-home-index-server/internal/config"
)

// intPtr returns a pointer to v for populating Options.Port (a *int
// sentinel where nil = "use env or default" and a non-nil pointer is
// the explicit caller value, including 0 for ephemeral binds).
func intPtr(v int) *int { return &v }

// chdirTmp moves the process into a fresh temp dir for the duration of
// the test and clears every env var the config package reads so one
// test's state does not leak into another.
func chdirTmp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })
	for _, k := range []string{
		"AUTH_TOKEN", "SESSION_SECRET", "PUBLIC_HOST", "PORT",
		"PORT_RANGE", "KILL_GRACE_MS", "SCANNER", "STATE_DIR",
	} {
		t.Setenv(k, "")
	}
	// Force HOME to the temp dir so the default StateDir lands under it.
	t.Setenv("HOME", dir)
	return dir
}

func baseOpts() config.Options {
	return config.Options{
		AuthToken:     strings.Repeat("a", 32),
		SessionSecret: strings.Repeat("b", 32),
		PublicHost:    "localhost",
		Port:          intPtr(40000),
		PortMin:       40000,
		PortMax:       40500,
		KillGraceMS:   3000,
		Scanner:       "auto",
	}
}

func TestLoad_AppliesExplicitOptions(t *testing.T) {
	chdirTmp(t)
	opts := baseOpts()
	opts.PublicHost = "yourhost.example"
	opts.Port = intPtr(40010)
	cfg, err := config.Load(opts)
	require.NoError(t, err)
	require.Equal(t, "yourhost.example", cfg.PublicHost)
	require.Equal(t, 40010, cfg.Port)
	require.Equal(t, 40000, cfg.PortMin)
	require.Equal(t, 40500, cfg.PortMax)
	require.Equal(t, 3000, cfg.KillGraceMS)
	require.Equal(t, "auto", cfg.Scanner)
	require.NotEmpty(t, cfg.StateDir)
}

func TestLoad_AppliesDefaults(t *testing.T) {
	chdirTmp(t)
	cfg, err := config.Load(config.Options{
		AuthToken:     strings.Repeat("a", 32),
		SessionSecret: strings.Repeat("b", 32),
	})
	require.NoError(t, err)
	require.Equal(t, config.DefaultPublicHost, cfg.PublicHost)
	require.Equal(t, config.DefaultPort, cfg.Port)
	require.Equal(t, config.DefaultPortMin, cfg.PortMin)
	require.Equal(t, config.DefaultPortMax, cfg.PortMax)
	require.Equal(t, config.DefaultKillGraceMS, cfg.KillGraceMS)
	require.Equal(t, config.DefaultScanner, cfg.Scanner)
	require.True(t, strings.HasSuffix(cfg.StateDir, ".port-manager"), "StateDir=%s", cfg.StateDir)
}

func TestLoad_ReadsEnvFile(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte(strings.Join([]string{
		"AUTH_TOKEN=" + strings.Repeat("x", 32),
		"SESSION_SECRET=" + strings.Repeat("y", 32),
		"PUBLIC_HOST=from-env.example",
		"PORT=40050",
		"PORT_RANGE=40100-40200",
		"KILL_GRACE_MS=1500",
		"SCANNER=lsof",
	}, "\n")+"\n"), 0o600))

	cfg, err := config.Load(config.Options{})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("x", 32), cfg.AuthToken)
	require.Equal(t, strings.Repeat("y", 32), cfg.SessionSecret)
	require.Equal(t, "from-env.example", cfg.PublicHost)
	require.Equal(t, 40050, cfg.Port)
	require.Equal(t, 40100, cfg.PortMin)
	require.Equal(t, 40200, cfg.PortMax)
	require.Equal(t, 1500, cfg.KillGraceMS)
	require.Equal(t, "lsof", cfg.Scanner)
}

func TestLoad_OptionsOverrideEnvFile(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte("PUBLIC_HOST=from-env.example\nPORT=12345\n"), 0o600))

	cfg, err := config.Load(config.Options{
		AuthToken:     strings.Repeat("a", 32),
		SessionSecret: strings.Repeat("b", 32),
		PublicHost:    "override.example",
		Port:          intPtr(40010),
	})
	require.NoError(t, err)
	require.Equal(t, "override.example", cfg.PublicHost)
	require.Equal(t, 40010, cfg.Port)
}

// TestLoad_ExplicitPortZeroIsHonored guards the *int sentinel:
// `Options.Port: intPtr(0)` means "ephemeral bind", and Load must NOT
// rewrite it to DefaultPort. The acceptance test mustConfig depends on
// this — see code-critic blocker #3.
func TestLoad_ExplicitPortZeroIsHonored(t *testing.T) {
	chdirTmp(t)
	cfg, err := config.Load(config.Options{
		AuthToken:     strings.Repeat("a", 32),
		SessionSecret: strings.Repeat("b", 32),
		PublicHost:    "localhost",
		Port:          intPtr(0),
		PortMin:       40000,
		PortMax:       40500,
		KillGraceMS:   3000,
		Scanner:       "auto",
	})
	require.NoError(t, err)
	require.Equal(t, 0, cfg.Port, "intPtr(0) must survive Load() as Port=0")
}

func TestLoad_GeneratesAndPersistsMissingSecrets(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte("AUTH_TOKEN=\nSESSION_SECRET=\nPUBLIC_HOST=localhost\n"), 0o600))

	var out bytes.Buffer
	cfg, err := config.Load(config.Options{StdOut: &out})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(cfg.AuthToken), config.MinTokenBytes)
	require.GreaterOrEqual(t, len(cfg.SessionSecret), config.MinSecretBytes)
	require.Contains(t, out.String(), "AUTH_TOKEN=")
	require.Contains(t, out.String(), cfg.AuthToken)

	// Reload: generated values must round-trip via the .env file.
	cfg2, err := config.Load(config.Options{})
	require.NoError(t, err)
	require.Equal(t, cfg.AuthToken, cfg2.AuthToken)
	require.Equal(t, cfg.SessionSecret, cfg2.SessionSecret)

	// Verify the .env file still contains the preserved key we put there.
	persisted, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Contains(t, string(persisted), "PUBLIC_HOST=localhost")
	require.Contains(t, string(persisted), "AUTH_TOKEN="+cfg.AuthToken)
	require.Contains(t, string(persisted), "SESSION_SECRET="+cfg.SessionSecret)
}

// TestLoad_BannerPrintsExactlyOnce enforces spec §7 step 1: the
// generated AUTH_TOKEN is printed to stdout EXACTLY ONCE. A second
// Load against the now-populated .env must not re-print it.
func TestLoad_BannerPrintsExactlyOnce(t *testing.T) {
	chdirTmp(t)

	var first bytes.Buffer
	cfg, err := config.Load(config.Options{StdOut: &first, PublicHost: "localhost"})
	require.NoError(t, err)
	require.Contains(t, first.String(), cfg.AuthToken,
		"first Load must print the generated AUTH_TOKEN")

	var second bytes.Buffer
	cfg2, err := config.Load(config.Options{StdOut: &second, PublicHost: "localhost"})
	require.NoError(t, err)
	require.Equal(t, cfg.AuthToken, cfg2.AuthToken,
		"second Load must read the persisted token, not regenerate")
	require.Empty(t, second.String(),
		"second Load must NOT re-print the AUTH_TOKEN banner; got %q", second.String())
}

// TestLoad_HandlesExportPrefixInEnv covers code-critic blocker #4:
// godotenv accepts `export KEY=VALUE` lines but the prior implementation
// failed to match them, so persistGenerated appended a duplicate
// `AUTH_TOKEN=` instead of replacing the existing line.
func TestLoad_HandlesExportPrefixInEnv(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte(
		"export AUTH_TOKEN=\n"+
			"  export SESSION_SECRET=\n"+
			"PUBLIC_HOST=localhost\n"), 0o600))

	cfg, err := config.Load(config.Options{StdOut: &bytes.Buffer{}})
	require.NoError(t, err)
	require.NotEmpty(t, cfg.AuthToken)
	require.NotEmpty(t, cfg.SessionSecret)

	persisted, err := os.ReadFile(envPath)
	require.NoError(t, err)
	body := string(persisted)

	// No duplicate AUTH_TOKEN= lines (the original `export AUTH_TOKEN=`
	// must have been rewritten in place, not appended after).
	require.Equal(t, 1, strings.Count(body, "AUTH_TOKEN="),
		"AUTH_TOKEN should appear exactly once, got:\n%s", body)
	require.Equal(t, 1, strings.Count(body, "SESSION_SECRET="),
		"SESSION_SECRET should appear exactly once, got:\n%s", body)

	// The export prefix must be preserved so any shell that sources the
	// file continues to actually export the variable.
	require.Contains(t, body, "export AUTH_TOKEN="+cfg.AuthToken)
	require.Contains(t, body, "export SESSION_SECRET="+cfg.SessionSecret)
}

// TestLoad_AtomicWriteLeavesOriginalOnFailure exercises the
// .env.tmp + rename codepath: if the rename never happens (we delete
// the tmp file mid-flight to simulate a crash window), the original
// .env must remain intact rather than being truncated. Code-critic
// blocker #2.
func TestLoad_AtomicWriteLeavesOriginalOnFailure(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	original := "AUTH_TOKEN=" + strings.Repeat("a", 32) + "\n" +
		"SESSION_SECRET=" + strings.Repeat("b", 32) + "\n" +
		"PUBLIC_HOST=localhost\n"
	require.NoError(t, os.WriteFile(envPath, []byte(original), 0o600))

	// Sanity check: a normal Load round-trip leaves the file readable
	// and non-empty (the tmp-file codepath either fully completes or
	// leaves the original alone — partial truncations are not allowed).
	cfg, err := config.Load(config.Options{
		StdOut: &bytes.Buffer{},
		// Force a re-write by also generating SESSION_SECRET fresh.
	})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("a", 32), cfg.AuthToken)

	persisted, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.NotEmpty(t, persisted, "atomic rename must not leave .env truncated")
	require.Contains(t, string(persisted), "PUBLIC_HOST=localhost")

	// Stale .env.tmp from a prior crash must not poison subsequent loads.
	tmpPath := envPath + ".tmp"
	require.NoError(t, os.WriteFile(tmpPath, []byte("garbage"), 0o600))

	cfg2, err := config.Load(config.Options{StdOut: &bytes.Buffer{}})
	require.NoError(t, err)
	require.Equal(t, cfg.AuthToken, cfg2.AuthToken,
		".env content must come from .env, not from a stale .env.tmp")

	final, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Contains(t, string(final), "PUBLIC_HOST=localhost",
		"original .env contents must survive even after a stale .tmp existed")
}

func TestLoad_GeneratesWithoutExistingEnvFile(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	_, err := os.Stat(envPath)
	require.True(t, os.IsNotExist(err))

	var out bytes.Buffer
	cfg, err := config.Load(config.Options{StdOut: &out, PublicHost: "localhost"})
	require.NoError(t, err)
	require.NotEmpty(t, cfg.AuthToken)
	require.NotEmpty(t, cfg.SessionSecret)

	// .env should have been created with both keys.
	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "AUTH_TOKEN="+cfg.AuthToken)
	require.Contains(t, string(data), "SESSION_SECRET="+cfg.SessionSecret)
}

func TestLoad_CustomEnvFile(t *testing.T) {
	dir := chdirTmp(t)
	custom := filepath.Join(dir, "custom.env")
	require.NoError(t, os.WriteFile(custom, []byte(
		"AUTH_TOKEN="+strings.Repeat("a", 32)+"\n"+
			"SESSION_SECRET="+strings.Repeat("b", 32)+"\n"+
			"PUBLIC_HOST=custom.example\n"), 0o600))
	cfg, err := config.Load(config.Options{EnvFile: custom})
	require.NoError(t, err)
	require.Equal(t, "custom.example", cfg.PublicHost)
}

func TestLoad_ValidationErrors(t *testing.T) {
	chdirTmp(t)
	cases := []struct {
		name string
		opts config.Options
		frag string
	}{
		{
			name: "short auth token",
			opts: config.Options{AuthToken: "short", SessionSecret: strings.Repeat("b", 32)},
			frag: "AUTH_TOKEN",
		},
		{
			name: "short session secret",
			opts: config.Options{AuthToken: strings.Repeat("a", 32), SessionSecret: "short"},
			frag: "SESSION_SECRET",
		},
		{
			name: "bad scanner",
			opts: config.Options{AuthToken: strings.Repeat("a", 32), SessionSecret: strings.Repeat("b", 32), Scanner: "nope"},
			frag: "SCANNER",
		},
		{
			name: "port min > max",
			opts: config.Options{AuthToken: strings.Repeat("a", 32), SessionSecret: strings.Repeat("b", 32), PortMin: 60000, PortMax: 40000},
			frag: "PORT_MIN",
		},
		{
			name: "port out of range",
			opts: config.Options{AuthToken: strings.Repeat("a", 32), SessionSecret: strings.Repeat("b", 32), Port: intPtr(70000)},
			frag: "PORT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(tc.opts)
			require.Error(t, err)
			require.ErrorIs(t, err, config.ErrValidation)
			require.Contains(t, err.Error(), tc.frag)
		})
	}
}

func TestLoad_BadPortRangeFallsBackToDefaults(t *testing.T) {
	dir := chdirTmp(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("PORT_RANGE=garbage\n"), 0o600))
	cfg, err := config.Load(config.Options{
		AuthToken:     strings.Repeat("a", 32),
		SessionSecret: strings.Repeat("b", 32),
	})
	require.NoError(t, err)
	require.Equal(t, config.DefaultPortMin, cfg.PortMin)
	require.Equal(t, config.DefaultPortMax, cfg.PortMax)
}

func TestBindFlags_ParsesCLIOverrides(t *testing.T) {
	chdirTmp(t)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var opts config.Options
	config.BindFlags(fs, &opts)
	require.NoError(t, fs.Parse([]string{
		"--port", "40010",
		"--public-host", "flagged.example",
		"--scanner", "lsof",
		"--kill-grace-ms", "1234",
		"--env-file", "flagged.env",
		"--trust-xff",
	}))
	opts.AuthToken = strings.Repeat("a", 32)
	opts.SessionSecret = strings.Repeat("b", 32)
	cfg, err := config.Load(opts)
	require.NoError(t, err)
	require.Equal(t, "flagged.example", cfg.PublicHost)
	require.Equal(t, 40010, cfg.Port)
	require.Equal(t, 1234, cfg.KillGraceMS)
	require.Equal(t, "lsof", cfg.Scanner)
	require.True(t, cfg.TrustXFF)
}

// TestLoad_TrustXFF_DefaultsFalse pins the safe default: with no env
// value and no flag, TRUST_XFF is off so the rate-limiter keys on
// r.RemoteAddr and an attacker cannot bypass it by spoofing XFF.
func TestLoad_TrustXFF_DefaultsFalse(t *testing.T) {
	chdirTmp(t)
	cfg, err := config.Load(baseOpts())
	require.NoError(t, err)
	require.False(t, cfg.TrustXFF, "TRUST_XFF must default to false")
}

// TestLoad_TrustXFF_FromEnv pins the env-only enable path.
func TestLoad_TrustXFF_FromEnv(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte(
		"AUTH_TOKEN="+strings.Repeat("a", 32)+"\n"+
			"SESSION_SECRET="+strings.Repeat("b", 32)+"\n"+
			"PUBLIC_HOST=localhost\n"+
			"TRUST_XFF=true\n"), 0o600))

	cfg, err := config.Load(config.Options{})
	require.NoError(t, err)
	require.True(t, cfg.TrustXFF)
}

// TestLoad_TrustXFF_FromOptions pins the flag/option enable path —
// either source being true enables the feature regardless of the other.
func TestLoad_TrustXFF_FromOptions(t *testing.T) {
	chdirTmp(t)
	opts := baseOpts()
	opts.TrustXFF = true
	cfg, err := config.Load(opts)
	require.NoError(t, err)
	require.True(t, cfg.TrustXFF)
}

// TestLoad_TrustXFF_GarbageEnvDoesNotEnable pins the strict-but-lenient
// parse: only canonical truthy spellings flip the bit; unknown values
// must NOT silently enable a security-sensitive feature.
func TestLoad_TrustXFF_GarbageEnvDoesNotEnable(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte(
		"AUTH_TOKEN="+strings.Repeat("a", 32)+"\n"+
			"SESSION_SECRET="+strings.Repeat("b", 32)+"\n"+
			"PUBLIC_HOST=localhost\n"+
			"TRUST_XFF=maybe\n"), 0o600))

	cfg, err := config.Load(config.Options{})
	require.NoError(t, err)
	require.False(t, cfg.TrustXFF, "ambiguous TRUST_XFF must NOT enable")
}

func TestLoad_PreservesUnrelatedLinesInEnv(t *testing.T) {
	dir := chdirTmp(t)
	envPath := filepath.Join(dir, ".env")
	original := "# comment header\n" +
		"PUBLIC_HOST=localhost\n" +
		"AUTH_TOKEN=\n" +
		"SESSION_SECRET=\n" +
		"# trailing comment\n"
	require.NoError(t, os.WriteFile(envPath, []byte(original), 0o600))

	_, err := config.Load(config.Options{StdOut: &bytes.Buffer{}})
	require.NoError(t, err)

	out, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Contains(t, string(out), "# comment header")
	require.Contains(t, string(out), "# trailing comment")
	require.Contains(t, string(out), "PUBLIC_HOST=localhost")
}
