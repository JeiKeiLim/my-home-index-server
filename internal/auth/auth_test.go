package auth

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/JeiKeiLim/my-home-index-server/internal/config"
)

func mustConfig() *config.Config {
	return &config.Config{
		AuthToken:     "test-" + strings.Repeat("a", 32),
		SessionSecret: "sec-" + strings.Repeat("b", 32),
	}
}

// fakeClock lets us move the auth package's notion of "now" forward
// deterministically for TTL / rate-limit window tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newWithClock(cfg *config.Config, t time.Time) (*Auth, *fakeClock) {
	clock := &fakeClock{t: t}
	return newAuth(cfg, clock.now), clock
}

// ---------------------------------------------------------------------
// Bearer checks
// ---------------------------------------------------------------------

// TestA6_EmptyTokenIsRejected mirrors tests/acceptance/security
// TestA6 — a short or empty bearer is rejected. Iron Law 4 (Harness).
func TestA6_EmptyTokenIsRejected(t *testing.T) {
	cfg := mustConfig()
	a := New(cfg)
	require.False(t, a.CheckBearer(""))
	require.False(t, a.CheckBearer("abc"))
}

// TestA7_CookieRotationInvalidatesSession mirrors tests/acceptance/security
// TestA7 — rotating SESSION_SECRET invalidates previously issued cookies.
// Spec §7, final paragraph.
func TestA7_CookieRotationInvalidatesSession(t *testing.T) {
	cfg1 := mustConfig()
	a1 := New(cfg1)
	cookie := a1.IssueCookie(time.Now())
	cfg2 := mustConfig()
	cfg2.SessionSecret = "different-secret-" + strings.Repeat("c", 32)
	a2 := New(cfg2)
	require.False(t, a2.VerifyCookie(cookie))
}

func TestCheckBearer_AcceptsExactMatch(t *testing.T) {
	cfg := mustConfig()
	a := New(cfg)
	require.True(t, a.CheckBearer(cfg.AuthToken))
}

func TestCheckBearer_RejectsEmptyAndShort(t *testing.T) {
	// Matches TestA6 from tests/acceptance/security/antiscenarios_test.go —
	// a core Iron Law 4 assertion.
	cfg := mustConfig()
	a := New(cfg)
	require.False(t, a.CheckBearer(""))
	require.False(t, a.CheckBearer("abc"))
}

func TestCheckBearer_RejectsMismatch(t *testing.T) {
	cfg := mustConfig()
	a := New(cfg)
	require.False(t, a.CheckBearer(cfg.AuthToken+"!"))
	require.False(t, a.CheckBearer("not-the-token-"+strings.Repeat("z", 32)))
}

func TestCheckBearer_RejectsWhenConfigTokenEmpty(t *testing.T) {
	cfg := &config.Config{AuthToken: "", SessionSecret: mustConfig().SessionSecret}
	a := New(cfg)
	require.False(t, a.CheckBearer(""))
	require.False(t, a.CheckBearer("anything"))
}

// ---------------------------------------------------------------------
// Cookie issue / verify
// ---------------------------------------------------------------------

func TestCookie_RoundTripWithinTTL(t *testing.T) {
	cfg := mustConfig()
	t0 := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	a, clock := newWithClock(cfg, t0)
	cookie := a.IssueCookie(t0)
	require.True(t, a.VerifyCookie(cookie))

	// Just before expiry → still valid.
	clock.advance(CookieTTL - time.Second)
	require.True(t, a.VerifyCookie(cookie))

	// Exactly at expiry → invalid (strict <).
	clock.advance(time.Second)
	require.False(t, a.VerifyCookie(cookie))
}

func TestCookie_RejectsAfterTTL(t *testing.T) {
	cfg := mustConfig()
	t0 := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	a, clock := newWithClock(cfg, t0)
	cookie := a.IssueCookie(t0)
	clock.advance(CookieTTL + time.Minute)
	require.False(t, a.VerifyCookie(cookie))
}

func TestCookie_RejectsEmptyAndMalformed(t *testing.T) {
	a := New(mustConfig())
	require.False(t, a.VerifyCookie(""))
	require.False(t, a.VerifyCookie("not-base64!@#"))
	// Valid base64, wrong length.
	short := base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3})
	require.False(t, a.VerifyCookie(short))
}

func TestCookie_RejectsTamperedMAC(t *testing.T) {
	cfg := mustConfig()
	a := New(cfg)
	cookie := a.IssueCookie(time.Now())
	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.RawURLEncoding.EncodeToString(raw)
	require.False(t, a.VerifyCookie(tampered))
}

func TestCookie_RejectsFutureIssuedAt(t *testing.T) {
	cfg := mustConfig()
	t0 := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	a, _ := newWithClock(cfg, t0)
	cookie := a.IssueCookie(t0.Add(time.Hour))
	require.False(t, a.VerifyCookie(cookie))
}

func TestCookie_RotatedSecretInvalidatesOldCookie(t *testing.T) {
	// Matches TestA7 from tests/acceptance/security/antiscenarios_test.go —
	// spec §7 last line: rotating SESSION_SECRET invalidates sessions.
	cfg1 := mustConfig()
	a1 := New(cfg1)
	cookie := a1.IssueCookie(time.Now())

	cfg2 := mustConfig()
	cfg2.SessionSecret = "rotated-" + strings.Repeat("c", 32)
	a2 := New(cfg2)
	require.False(t, a2.VerifyCookie(cookie))
}

func TestCookie_LayoutIsBase64EncodedTSAndMAC(t *testing.T) {
	cfg := mustConfig()
	a := New(cfg)
	t0 := time.Unix(1700000000, 0)
	cookie := a.IssueCookie(t0)
	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	require.NoError(t, err)
	require.Equal(t, cookieBodyBytes, len(raw))
	require.Equal(t, uint64(1700000000), binary.BigEndian.Uint64(raw[:cookieTimestampBytes]))
}

// ---------------------------------------------------------------------
// Rate limiter
// ---------------------------------------------------------------------

func TestRateLimit_AllowsUnderCapacity(t *testing.T) {
	t0 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	a, _ := newWithClock(mustConfig(), t0)
	for i := 0; i < RateLimitCapacity-1; i++ {
		a.RateLimitRecordFailure("1.2.3.4")
	}
	ok, _ := a.RateLimitCheck("1.2.3.4")
	require.True(t, ok)
}

func TestRateLimit_BlocksAtCapacity(t *testing.T) {
	t0 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	a, _ := newWithClock(mustConfig(), t0)
	for i := 0; i < RateLimitCapacity; i++ {
		a.RateLimitRecordFailure("1.2.3.4")
	}
	ok, retry := a.RateLimitCheck("1.2.3.4")
	require.False(t, ok)
	require.Greater(t, retry, time.Duration(0))
	require.LessOrEqual(t, retry, RateLimitWindow)
	// Tighter bound: retryAfter must be close to the full window. A
	// buggy implementation that reports a 1-second lockout (e.g. uses
	// `now.Sub(oldest)` instead of `window - now.Sub(oldest)`) would
	// fail this assertion. Spec §9 #11.
	require.Greater(t, retry, 14*time.Minute,
		"lockout must be close to the 15-minute window, got %s", retry)
}

// TestRateLimit_LockedIPStaysBlockedForFullWindow asserts that a
// blocked IP recording further failures does not surprisingly extend
// or reset its window — the original 15-minute lockout still expires
// at t0+window. Code-critic test sharpening (c).
func TestRateLimit_LockedIPStaysBlockedForFullWindow(t *testing.T) {
	t0 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	a, clock := newWithClock(mustConfig(), t0)

	// Push the IP exactly to capacity at t0 — these are the failures
	// whose ages drive the lockout window.
	for i := 0; i < RateLimitCapacity; i++ {
		a.RateLimitRecordFailure("1.2.3.4")
	}
	blocked, _ := a.RateLimitCheck("1.2.3.4")
	require.False(t, blocked)

	// Attacker keeps hammering: each new failure must NOT slide the
	// floor of the window forward (i.e. must not remove the oldest
	// in-window failure ahead of schedule).
	clock.advance(5 * time.Minute)
	a.RateLimitRecordFailure("1.2.3.4")
	a.RateLimitRecordFailure("1.2.3.4")
	stillBlocked, retry := a.RateLimitCheck("1.2.3.4")
	require.False(t, stillBlocked,
		"recording extra failures must not unblock the IP")
	require.Greater(t, retry, time.Duration(0))
	require.LessOrEqual(t, retry, 10*time.Minute+time.Second,
		"window must keep counting down; got retry=%s", retry)

	// At t0 + window the original failures age out and access resumes.
	clock.advance(10*time.Minute + time.Second)
	ok, _ := a.RateLimitCheck("1.2.3.4")
	require.True(t, ok,
		"IP must regain access exactly when the original window elapses")
}

// TestRateLimit_BoundedByMaxIPs covers code-critic blocker #1: a
// stream of distinct source IPs (e.g. botnet rotation) must not grow
// the failures map without bound. We insert 2*RateLimitMaxIPs distinct
// IPs and assert the map stayed at-or-below the cap throughout.
func TestRateLimit_BoundedByMaxIPs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 20k-IP eviction test in -short mode")
	}
	t0 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	a, _ := newWithClock(mustConfig(), t0)
	const totalIPs = 2 * RateLimitMaxIPs
	for i := 0; i < totalIPs; i++ {
		a.RateLimitRecordFailure(fmt.Sprintf("10.%d.%d.%d",
			(i>>16)&0xff, (i>>8)&0xff, i&0xff))
	}
	a.rateLimitMu.Lock()
	size := len(a.failures)
	a.rateLimitMu.Unlock()
	require.LessOrEqual(t, size, RateLimitMaxIPs,
		"failures map must stay bounded by RateLimitMaxIPs (%d), got %d",
		RateLimitMaxIPs, size)
}

func TestRateLimit_WindowSlides(t *testing.T) {
	t0 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	a, clock := newWithClock(mustConfig(), t0)
	for i := 0; i < RateLimitCapacity; i++ {
		a.RateLimitRecordFailure("1.2.3.4")
	}
	blocked, _ := a.RateLimitCheck("1.2.3.4")
	require.False(t, blocked)

	// Advance beyond the window — the old failures age out.
	clock.advance(RateLimitWindow + time.Second)
	ok, retry := a.RateLimitCheck("1.2.3.4")
	require.True(t, ok)
	require.Equal(t, time.Duration(0), retry)
}

func TestRateLimit_SuccessResetsCounter(t *testing.T) {
	t0 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	a, _ := newWithClock(mustConfig(), t0)
	for i := 0; i < RateLimitCapacity; i++ {
		a.RateLimitRecordFailure("1.2.3.4")
	}
	blocked, _ := a.RateLimitCheck("1.2.3.4")
	require.False(t, blocked)

	a.RateLimitRecordSuccess("1.2.3.4")
	ok, _ := a.RateLimitCheck("1.2.3.4")
	require.True(t, ok)
}

func TestRateLimit_IsolatesByIP(t *testing.T) {
	t0 := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	a, _ := newWithClock(mustConfig(), t0)
	for i := 0; i < RateLimitCapacity; i++ {
		a.RateLimitRecordFailure("1.1.1.1")
	}
	blocked, _ := a.RateLimitCheck("1.1.1.1")
	require.False(t, blocked)

	ok, _ := a.RateLimitCheck("2.2.2.2")
	require.True(t, ok, "other IPs are unaffected")
}

func TestRateLimit_IsRaceSafe(t *testing.T) {
	a := New(mustConfig())
	var wg sync.WaitGroup
	var allowedCount int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := a.RateLimitCheck("1.2.3.4"); ok {
				atomic.AddInt64(&allowedCount, 1)
			}
			a.RateLimitRecordFailure("1.2.3.4")
		}()
	}
	wg.Wait()
	// No assertions on exact counts — -race verifies mutual exclusion.
	require.NotNil(t, a)
}
