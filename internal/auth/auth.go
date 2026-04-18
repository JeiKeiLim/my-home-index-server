// Package auth implements token and session authentication for
// port-manager: constant-time bearer compare, HMAC-SHA256 signed session
// cookies with a 24h TTL, and a per-IP sliding-window rate limiter.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"sync"
	"time"

	"github.com/JeiKeiLim/my-home-index-server/internal/config"
)

// Cookie / rate-limit parameters. Values chosen per spec §7 (Auth Flow)
// and §9 #11 (Rate limit).
const (
	// CookieTTL bounds the validity of a session cookie relative to its
	// issuedAt stamp.
	CookieTTL = 24 * time.Hour
	// RateLimitWindow is the sliding window over which failures are
	// counted per remote IP.
	RateLimitWindow = 15 * time.Minute
	// RateLimitCapacity is the max allowed failures within the window
	// before subsequent attempts are refused.
	RateLimitCapacity = 5
	// RateLimitMaxIPs caps the failures map so a stream of distinct
	// source IPs (e.g. botnet rotation) cannot exhaust dashboard memory.
	// When the cap is reached a new entry evicts the oldest first-seen
	// IP that no longer has live failures in its window.
	RateLimitMaxIPs = 10000

	cookieTimestampBytes = 8
	cookieMacBytes       = sha256.Size
	cookieBodyBytes      = cookieTimestampBytes + cookieMacBytes
)

// Auth bundles the credential, cookie, and rate-limit logic. It holds no
// external resources; construct one with New and share it across the
// request handlers.
type Auth struct {
	token       []byte
	secret      []byte
	now         func() time.Time
	rateLimitMu sync.Mutex
	failures    map[string][]time.Time
}

// New builds an Auth from the validated Config. The Auth keeps its own
// copy of the token and secret; later mutation of cfg does not affect
// previously-issued Auth instances.
func New(cfg *config.Config) *Auth {
	return newAuth(cfg, time.Now)
}

func newAuth(cfg *config.Config, now func() time.Time) *Auth {
	return &Auth{
		token:    []byte(cfg.AuthToken),
		secret:   []byte(cfg.SessionSecret),
		now:      now,
		failures: map[string][]time.Time{},
	}
}

// CheckBearer reports whether the supplied token matches the
// configured AUTH_TOKEN. Comparison is constant-time and rejects empty
// or sub-MinTokenBytes input outright (there is no "zero-length token"
// identity, and tokens shorter than the configured minimum are
// rejected by contract — the explicit length guard makes the policy
// observable rather than incidental to a SHA-256 mismatch).
func (a *Auth) CheckBearer(token string) bool {
	if len(a.token) == 0 {
		return false
	}
	// Length check is on caller-supplied input only, so it does not leak
	// timing information about the secret.
	if len(token) < config.MinTokenBytes {
		return false
	}
	// Hash both sides to fixed length so the comparison cost does not
	// reveal the true token length to an attacker.
	want := sha256.Sum256(a.token)
	got := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

// IssueCookie returns a fresh pm_session cookie value for the given
// issuance time. Layout is base64url( issuedAt[8]be || HMAC-SHA256 ).
func (a *Auth) IssueCookie(issued time.Time) string {
	buf := make([]byte, cookieBodyBytes)
	binary.BigEndian.PutUint64(buf[:cookieTimestampBytes], uint64(issued.Unix()))
	mac := hmac.New(sha256.New, a.secret)
	mac.Write(buf[:cookieTimestampBytes])
	copy(buf[cookieTimestampBytes:], mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(buf)
}

// VerifyCookie reports whether the cookie was signed by the current
// SESSION_SECRET and is within CookieTTL of now. Any decoding error,
// MAC mismatch, or expired stamp returns false.
func (a *Auth) VerifyCookie(value string) bool {
	if value == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != cookieBodyBytes {
		return false
	}
	ts := binary.BigEndian.Uint64(raw[:cookieTimestampBytes])
	want := hmac.New(sha256.New, a.secret)
	want.Write(raw[:cookieTimestampBytes])
	expected := want.Sum(nil)
	if subtle.ConstantTimeCompare(expected, raw[cookieTimestampBytes:]) != 1 {
		return false
	}
	issued := time.Unix(int64(ts), 0)
	now := a.now()
	if now.Before(issued) {
		// Clock skew or tampered future timestamp — reject.
		return false
	}
	return now.Sub(issued) < CookieTTL
}

// RateLimitCheck reports whether a login attempt from ip may proceed.
// When allowed==false, retryAfter is the duration until the oldest
// recorded failure falls out of the sliding window.
func (a *Auth) RateLimitCheck(ip string) (allowed bool, retryAfter time.Duration) {
	a.rateLimitMu.Lock()
	defer a.rateLimitMu.Unlock()
	now := a.now()
	fails := a.pruneLocked(ip, now)
	if len(fails) >= RateLimitCapacity {
		oldest := fails[0]
		wait := RateLimitWindow - now.Sub(oldest)
		if wait < 0 {
			wait = 0
		}
		return false, wait
	}
	return true, 0
}

// RateLimitRecordFailure appends a failure timestamp for ip, pruning
// expired entries first so the slice stays bounded.
func (a *Auth) RateLimitRecordFailure(ip string) {
	a.rateLimitMu.Lock()
	defer a.rateLimitMu.Unlock()
	now := a.now()
	fails := a.pruneLocked(ip, now)
	if _, exists := a.failures[ip]; !exists && len(a.failures) >= RateLimitMaxIPs {
		// New IP would push us over the cap — sweep the whole map and
		// drop every entry whose window has elapsed. If that does not
		// free at least one slot, evict the IP whose newest failure is
		// the oldest (i.e. the IP closest to falling out of the window
		// anyway). Both operate on caller-supplied IPs only and never
		// reset the live attacker's counter.
		a.sweepExpiredLocked(now)
		if len(a.failures) >= RateLimitMaxIPs {
			a.evictOldestLocked()
		}
	}
	a.failures[ip] = append(fails, now)
}

// RateLimitRecordSuccess clears any accumulated failures for ip. A
// successful login resets the counter per spec §9 #11.
func (a *Auth) RateLimitRecordSuccess(ip string) {
	a.rateLimitMu.Lock()
	defer a.rateLimitMu.Unlock()
	delete(a.failures, ip)
}

// pruneLocked drops failure timestamps older than the sliding window
// and returns the surviving slice. Caller must hold rateLimitMu.
func (a *Auth) pruneLocked(ip string, now time.Time) []time.Time {
	fails := a.failures[ip]
	if len(fails) == 0 {
		return nil
	}
	cutoff := now.Add(-RateLimitWindow)
	keep := fails[:0]
	for _, t := range fails {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(a.failures, ip)
		return nil
	}
	a.failures[ip] = keep
	return keep
}

// sweepExpiredLocked walks the entire failures map and drops any IP
// whose newest failure is already outside the window. Caller must hold
// rateLimitMu.
func (a *Auth) sweepExpiredLocked(now time.Time) {
	cutoff := now.Add(-RateLimitWindow)
	for ip, fails := range a.failures {
		if len(fails) == 0 || !fails[len(fails)-1].After(cutoff) {
			delete(a.failures, ip)
		}
	}
}

// evictOldestLocked removes the IP whose most-recent failure is oldest.
// Used as a last-resort eviction when the map is full of live
// (in-window) entries. Caller must hold rateLimitMu.
func (a *Auth) evictOldestLocked() {
	var (
		oldestIP   string
		oldestTime time.Time
		seen       bool
	)
	for ip, fails := range a.failures {
		if len(fails) == 0 {
			delete(a.failures, ip)
			continue
		}
		newest := fails[len(fails)-1]
		if !seen || newest.Before(oldestTime) {
			oldestIP = ip
			oldestTime = newest
			seen = true
		}
	}
	if seen {
		delete(a.failures, oldestIP)
	}
}
