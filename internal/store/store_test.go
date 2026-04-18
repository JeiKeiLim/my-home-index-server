package store_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/JeiKeiLim/my-home-index-server/internal/store"
)

// statePath returns the absolute path to the state file under homeDir.
func statePath(homeDir string) string {
	return filepath.Join(homeDir, ".port-manager", "state.json")
}

func tmpPath(homeDir string) string {
	return statePath(homeDir) + ".tmp"
}

// readState parses the on-disk state file and returns the decoded view.
func readState(t *testing.T, homeDir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(statePath(homeDir))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func TestOpenCreatesStateDirAndIsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)
	require.NotNil(t, s)

	// Directory exists even before the first mutation.
	info, err := os.Stat(filepath.Join(dir, ".port-manager"))
	require.NoError(t, err)
	require.True(t, info.IsDir())

	label, err := s.Label("/tmp", []string{"cmd"})
	require.NoError(t, err)
	require.Equal(t, "", label, "label lookup on empty store must return empty string")

	remembered, err := s.ListRemembered("/tmp", []string{"cmd"})
	require.NoError(t, err)
	require.Empty(t, remembered)
}

func TestOpenRejectsEmptyHomeDir(t *testing.T) {
	_, err := store.Open("")
	require.Error(t, err)
}

func TestSetLabelRoundTripsAndPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)

	require.NoError(t, s.SetLabel("/home/u/proj", []string{"npm", "run", "dev"}, "blog-dev"))

	got, err := s.Label("/home/u/proj", []string{"npm", "run", "dev"})
	require.NoError(t, err)
	require.Equal(t, "blog-dev", got)

	// A fresh Open must see the persisted label.
	s2, err := store.Open(dir)
	require.NoError(t, err)
	got2, err := s2.Label("/home/u/proj", []string{"npm", "run", "dev"})
	require.NoError(t, err)
	require.Equal(t, "blog-dev", got2)
}

func TestSetLabelEmptyClearsEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)

	require.NoError(t, s.SetLabel("/tmp/a", []string{"cmd"}, "hello"))
	require.NoError(t, s.SetLabel("/tmp/a", []string{"cmd"}, ""))
	got, err := s.Label("/tmp/a", []string{"cmd"})
	require.NoError(t, err)
	require.Equal(t, "", got, "empty label must clear the stored entry")
}

func TestSetLabelRejectsOverLengthLabel(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	err = s.SetLabel("/tmp/a", []string{"cmd"}, strings.Repeat("x", store.MaxLabelLen+1))
	require.ErrorIs(t, err, store.ErrLabelTooLong)

	// A 64-char label is accepted on the boundary.
	require.NoError(t, s.SetLabel("/tmp/a", []string{"cmd"}, strings.Repeat("x", store.MaxLabelLen)))
	got, err := s.Label("/tmp/a", []string{"cmd"})
	require.NoError(t, err)
	require.Len(t, got, store.MaxLabelLen)
}

// TestSetLabelLengthCheckIsRuneBasedNotBytes pins the F2 fix: a
// 64-rune multibyte label is accepted (raw byte length > 64), a
// 65-rune label is rejected. Before the fix the store measured
// len(label) in bytes, disagreeing with the handler's utf8.RuneCount
// check — a 64-rune emoji label would 200 at the handler then fail
// with ErrLabelTooLong when it hit persist.
func TestSetLabelLengthCheckIsRuneBasedNotBytes(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	// 64 runes of a 4-byte codepoint → 256 bytes on the wire.
	sixtyFourRunes := strings.Repeat("😀", store.MaxLabelLen)
	require.Equal(t, store.MaxLabelLen, len([]rune(sixtyFourRunes)))
	require.NoError(t, s.SetLabel("/tmp/a", []string{"cmd"}, sixtyFourRunes),
		"64-rune multibyte label must be accepted on the boundary")
	got, err := s.Label("/tmp/a", []string{"cmd"})
	require.NoError(t, err)
	require.Equal(t, sixtyFourRunes, got)

	// 65 runes exceeds MaxLabelLen and must be rejected.
	sixtyFive := strings.Repeat("😀", store.MaxLabelLen+1)
	require.ErrorIs(t,
		s.SetLabel("/tmp/a", []string{"cmd"}, sixtyFive),
		store.ErrLabelTooLong,
		"65-rune label must be rejected by the store")

	// Boundary with Hangul too — sanity check the rule holds for a
	// 3-byte codepoint not just 4-byte emoji.
	sixtyFourHangul := strings.Repeat("가", store.MaxLabelLen)
	require.Equal(t, store.MaxLabelLen, len([]rune(sixtyFourHangul)))
	require.NoError(t, s.SetLabel("/tmp/b", []string{"cmd"}, sixtyFourHangul))
}

func TestLabelKeyIsStableAndShort(t *testing.T) {
	k := store.LabelKey("/tmp/a", []string{"cmd"})
	require.Len(t, k, 16, "label key must be 16 hex chars")
	// Deterministic.
	require.Equal(t, k, store.LabelKey("/tmp/a", []string{"cmd"}))
	// Different inputs produce different keys.
	require.NotEqual(t, k, store.LabelKey("/tmp/b", []string{"cmd"}))
	require.NotEqual(t, k, store.LabelKey("/tmp/a", []string{"cmd2"}))
}

// TestLabelKeyDistinguishesArgvGroupings asserts the null-byte separator
// between argv elements prevents ["ls -l"] from colliding with ["ls", "-l"].
// Joining on spaces or any printable delimiter would produce identical
// hash input and silently merge two distinct commands.
func TestLabelKeyDistinguishesArgvGroupings(t *testing.T) {
	single := store.LabelKey("/w", []string{"ls -l"})
	split := store.LabelKey("/w", []string{"ls", "-l"})
	require.NotEqual(t, single, split, "argv groupings must hash distinctly")
}

// TestA5 — 100 concurrent SetLabel calls on the same (cwd, cmd) must
// serialise cleanly: the state file remains parseable, no .tmp file is
// left behind, and a re-Open can read the label back.
func TestA5(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)

	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, s.SetLabel("/tmp/x", []string{"/bin/foo"}, fmt.Sprintf("label-%c", 'A'+i%26)))
		}(i)
	}
	wg.Wait()

	// State file is valid JSON.
	state := readState(t, dir)
	require.Equal(t, float64(1), state["version"])

	// Re-open and read the label; exact value is last-write-wins.
	s2, err := store.Open(dir)
	require.NoError(t, err)
	label, err := s2.Label("/tmp/x", []string{"/bin/foo"})
	require.NoError(t, err)
	require.NotEmpty(t, label)
	require.True(t, strings.HasPrefix(label, "label-"))

	// No stray .tmp left behind.
	_, err = os.Stat(tmpPath(dir))
	require.True(t, os.IsNotExist(err), "state.json.tmp must not be present after successful writes")
}

// TestA15 — a stale state.json.tmp from a prior crashed write must be
// ignored. The committed state.json is the source of truth on Open.
func TestA15(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)

	require.NoError(t, s.SetLabel("/tmp", []string{"cmd"}, "name"))

	// Simulate a partially-written tmp file from a prior crash.
	require.NoError(t, os.WriteFile(tmpPath(dir), []byte("{garbage"), 0o600))

	// Reopen — state.json (not the tmp) must be used.
	s2, err := store.Open(dir)
	require.NoError(t, err)
	got, err := s2.Label("/tmp", []string{"cmd"})
	require.NoError(t, err)
	require.Equal(t, "name", got)
}

// TestA17 — inserting 200 remembered entries for the same (cwd, cmd)
// must leave only the most recent PerKeyRememberedCap visible.
func TestA17(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)

	for i := 0; i < 200; i++ {
		require.NoError(t, s.Remember(store.Remembered{
			Port:    40000 + i%500,
			Command: []string{"cmd"},
			Cwd:     "/tmp/a",
		}))
	}

	r, err := s.ListRemembered("/tmp/a", []string{"cmd"})
	require.NoError(t, err)
	require.LessOrEqual(t, len(r), store.PerKeyRememberedCap, "per-key retention cap is 5")
	require.Equal(t, store.PerKeyRememberedCap, len(r))

	// Survives a reopen.
	s2, err := store.Open(dir)
	require.NoError(t, err)
	r2, err := s2.ListRemembered("/tmp/a", []string{"cmd"})
	require.NoError(t, err)
	require.Equal(t, store.PerKeyRememberedCap, len(r2))
}

func TestRememberAssignsIDAndLabelKeyAndKilledAt(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	before := time.Now().Add(-time.Second)
	require.NoError(t, s.Remember(store.Remembered{
		Port: 40100, Command: []string{"python", "worker.py"}, Cwd: "/work",
	}))
	after := time.Now().Add(time.Second)

	list, err := s.ListRemembered("/work", []string{"python", "worker.py"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	got := list[0]
	require.NotEmpty(t, got.ID, "Remember must assign a ULID when caller leaves ID empty")
	require.Len(t, got.ID, 26, "ULID must be 26 Crockford base32 chars")
	require.Equal(t, store.LabelKey("/work", []string{"python", "worker.py"}), got.LabelKey)
	require.False(t, got.KilledAt.IsZero())
	require.True(t, !got.KilledAt.Before(before) && !got.KilledAt.After(after),
		"KilledAt must be recent: %v vs [%v, %v]", got.KilledAt, before, after)
}

func TestListRememberedReturnsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		require.NoError(t, s.Remember(store.Remembered{
			Port: 40100 + i, Command: []string{"cmd"}, Cwd: "/work",
		}))
	}

	list, err := s.ListRemembered("/work", []string{"cmd"})
	require.NoError(t, err)
	require.Len(t, list, 3)
	// Newest first → last inserted (port 40102) appears first.
	require.Equal(t, 40102, list[0].Port)
	require.Equal(t, 40101, list[1].Port)
	require.Equal(t, 40100, list[2].Port)
}

func TestFindAndDeleteRemembered(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	require.NoError(t, s.Remember(store.Remembered{Port: 40100, Command: []string{"cmd"}, Cwd: "/w"}))
	require.NoError(t, s.Remember(store.Remembered{Port: 40101, Command: []string{"cmd"}, Cwd: "/w"}))

	list, err := s.ListRemembered("/w", []string{"cmd"})
	require.NoError(t, err)
	require.Len(t, list, 2)

	target := list[0].ID
	found, err := s.FindRemembered(target)
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Equal(t, target, found.ID)

	// Missing id returns (nil, nil).
	missing, err := s.FindRemembered("does-not-exist")
	require.NoError(t, err)
	require.Nil(t, missing)

	require.NoError(t, s.DeleteRemembered(target))
	require.NoError(t, s.DeleteRemembered(target), "DeleteRemembered must be idempotent")

	found2, err := s.FindRemembered(target)
	require.NoError(t, err)
	require.Nil(t, found2)

	list2, err := s.ListRemembered("/w", []string{"cmd"})
	require.NoError(t, err)
	require.Len(t, list2, 1)
}

func TestGlobalRememberedCapIsEnforced(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	// Use a different (cwd, command) per insert so the per-key cap
	// does NOT kick in and we can observe the global cap directly.
	total := store.GlobalRememberedCap + 25
	for i := 0; i < total; i++ {
		require.NoError(t, s.Remember(store.Remembered{
			Port:    40100,
			Command: []string{"cmd"},
			Cwd:     fmt.Sprintf("/w/%d", i),
		}))
	}

	// Sum entries across all keys.
	b, err := os.ReadFile(statePath(dir))
	require.NoError(t, err)
	var p struct {
		Remembered []store.Remembered `json:"remembered"`
	}
	require.NoError(t, json.Unmarshal(b, &p))
	require.LessOrEqual(t, len(p.Remembered), store.GlobalRememberedCap,
		"global cap must bound total remembered entries")
	require.Equal(t, store.GlobalRememberedCap, len(p.Remembered))

	// The 25 oldest entries (i=0..24) must have been evicted — i=25
	// is the oldest surviving entry.
	oldestSurvivingCwd := fmt.Sprintf("/w/%d", total-store.GlobalRememberedCap)
	list, err := s.ListRemembered(oldestSurvivingCwd, []string{"cmd"})
	require.NoError(t, err)
	require.Len(t, list, 1)

	evictedCwd := "/w/0"
	list0, err := s.ListRemembered(evictedCwd, []string{"cmd"})
	require.NoError(t, err)
	require.Empty(t, list0)
}

func TestStateSizeStaysUnder256KiB(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	// Inject large env blobs so each entry is ≳ 2 KiB. With the global
	// cap of 100 entries, naive math is ~200 KiB which is under 256; use
	// 100 keys × 5 per-key = 500 slots worth of inserts with 4 KiB env
	// apiece to confirm the MaxStateBytes fallback eviction path also
	// works when an individual entry is unusually large.
	bigEnv := []string{strings.Repeat("PAYLOAD=", 512)} // ~4 KiB per entry
	for i := 0; i < 120; i++ {
		require.NoError(t, s.Remember(store.Remembered{
			Port:    40100,
			Command: []string{"cmd"},
			Cwd:     fmt.Sprintf("/w/%d", i),
			Env:     bigEnv,
		}))
	}

	info, err := os.Stat(statePath(dir))
	require.NoError(t, err)
	require.LessOrEqual(t, info.Size(), int64(store.MaxStateBytes),
		"Iron Law 13: persisted state.json must be ≤ 256 KiB; got %d bytes", info.Size())
}

// TestLabelsAloneCapEnforced exercises the F2 eviction path: with no
// remembered entries, creating enough labels to blow past MaxStateBytes
// must cause persist to evict the oldest labels until the on-disk file
// fits, rather than silently writing an oversized state file.
//
// Uses a long cwd and max-length label per entry so the cap is exceeded
// with a modest number of iterations (keeping the test fast under -race
// -count=5). A bare `/w/%d` cwd + `label-%d` would need ~5000 iterations
// — prohibitive under race detection with fsync on every write.
func TestLabelsAloneCapEnforced(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	// ~300 bytes per persisted label entry after JSON pretty-print;
	// 1200 iterations (with eviction kicking in partway) easily exceeds
	// 256 KiB if uncapped.
	longCwd := "/w/" + strings.Repeat("x", 150)
	longLabel := strings.Repeat("y", store.MaxLabelLen)
	const total = 1200
	for i := 0; i < total; i++ {
		require.NoError(t, s.SetLabel(
			fmt.Sprintf("%s/%d", longCwd, i),
			[]string{"cmd"},
			longLabel,
		))
	}

	info, err := os.Stat(statePath(dir))
	require.NoError(t, err)
	require.LessOrEqual(t, info.Size(), int64(store.MaxStateBytes),
		"labels alone must not cause state.json to exceed MaxStateBytes; got %d bytes", info.Size())

	// Eviction actually ran — the on-disk file must contain fewer than
	// `total` label entries.
	b, err := os.ReadFile(statePath(dir))
	require.NoError(t, err)
	var parsed struct {
		Labels map[string]json.RawMessage `json:"labels"`
	}
	require.NoError(t, json.Unmarshal(b, &parsed))
	require.Less(t, len(parsed.Labels), total, "eviction must have dropped some labels")

	// Store is still functional after heavy eviction.
	require.NoError(t, s.SetLabel("/post-evict", []string{"cmd"}, "still-works"))
	got, err := s.Label("/post-evict", []string{"cmd"})
	require.NoError(t, err)
	require.Equal(t, "still-works", got)
}

// TestListRememberedReturnsDeepCopy asserts F3: mutating the slice
// fields (Env, Command) on a returned Remembered must NOT rewrite the
// store's backing arrays.
func TestListRememberedReturnsDeepCopy(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	require.NoError(t, s.Remember(store.Remembered{
		Port:    40100,
		Command: []string{"python", "worker.py"},
		Cwd:     "/work",
		Env:     []string{"PYTHONPATH=/opt", "LANG=C"},
	}))

	list1, err := s.ListRemembered("/work", []string{"python", "worker.py"})
	require.NoError(t, err)
	require.Len(t, list1, 1)

	// Caller mutates returned slices — this must be harmless.
	list1[0].Env[0] = "HIJACKED=yes"
	list1[0].Command[0] = "rm"

	// A subsequent List still sees the originals.
	list2, err := s.ListRemembered("/work", []string{"python", "worker.py"})
	require.NoError(t, err)
	require.Len(t, list2, 1)
	require.Equal(t, "PYTHONPATH=/opt", list2[0].Env[0])
	require.Equal(t, "python", list2[0].Command[0])

	// Same for Find.
	found, err := s.FindRemembered(list2[0].ID)
	require.NoError(t, err)
	require.NotNil(t, found)
	found.Env[0] = "HIJACKED=yes"
	found.Command[0] = "rm"

	list3, err := s.ListRemembered("/work", []string{"python", "worker.py"})
	require.NoError(t, err)
	require.Equal(t, "PYTHONPATH=/opt", list3[0].Env[0])
	require.Equal(t, "python", list3[0].Command[0])
}

// TestRememberDeepCopiesInputSlices asserts the caller-side invariant:
// mutating the argv or env slice you handed to Remember must not
// retroactively corrupt the stored entry.
func TestRememberDeepCopiesInputSlices(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	cmd := []string{"python", "worker.py"}
	env := []string{"PYTHONPATH=/opt"}
	require.NoError(t, s.Remember(store.Remembered{
		Port: 40100, Command: cmd, Cwd: "/work", Env: env,
	}))

	cmd[0] = "rm"
	env[0] = "HIJACKED=yes"

	list, err := s.ListRemembered("/work", []string{"python", "worker.py"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "python", list[0].Command[0])
	require.Equal(t, "PYTHONPATH=/opt", list[0].Env[0])
}

func TestRestoredStateReadsPriorRemembered(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	require.NoError(t, s.Remember(store.Remembered{
		Port: 40100, Command: []string{"python", "worker.py"}, Cwd: "/work",
		Env: []string{"PYTHONPATH=/opt"},
	}))

	// Reopen and confirm remembered entry round-trips.
	s2, err := store.Open(dir)
	require.NoError(t, err)
	list, err := s2.ListRemembered("/work", []string{"python", "worker.py"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, 40100, list[0].Port)
	require.Equal(t, []string{"PYTHONPATH=/opt"}, list[0].Env)
	require.Equal(t, []string{"python", "worker.py"}, list[0].Command)
	require.Equal(t, "/work", list[0].Cwd)
	require.NotEmpty(t, list[0].ID)
}

// TestCommandArgsWithSpacesRoundTrip — args containing whitespace or
// quotes must round-trip through the JSON store without shell-level
// lossy splitting. This is the core motivation for Command []string.
func TestCommandArgsWithSpacesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	cmd := []string{"/usr/bin/env", "sh", "-c", `echo "hello world"`}
	require.NoError(t, s.Remember(store.Remembered{
		Port: 40100, Command: cmd, Cwd: "/w",
	}))

	s2, err := store.Open(dir)
	require.NoError(t, err)
	list, err := s2.ListRemembered("/w", cmd)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, cmd, list[0].Command)
}

func TestHydrateRejectsCorruptStateFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".port-manager"), 0o755))
	require.NoError(t, os.WriteFile(statePath(dir), []byte("{not-json"), 0o600))

	_, err := store.Open(dir)
	require.Error(t, err, "Open must surface a parse error on corrupt state.json")
}

func TestOpenIsIdempotentWhenStateDirAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".port-manager"), 0o755))
	s, err := store.Open(dir)
	require.NoError(t, err)
	require.NoError(t, s.SetLabel("/tmp", []string{"cmd"}, "ok"))
}

// TestSetLabelWritesAreAtomic asserts the tmp-then-rename sequence: a
// concurrent reader that keeps opening state.json never sees a
// truncated/partial file.
func TestSetLabelWritesAreAtomic(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)
	// Seed one successful write so state.json exists.
	require.NoError(t, s.SetLabel("/tmp", []string{"cmd"}, "seed"))

	stop := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				close(errCh)
				return
			default:
			}
			b, err := os.ReadFile(statePath(dir))
			if err != nil {
				errCh <- err
				return
			}
			var p map[string]any
			if err := json.Unmarshal(b, &p); err != nil {
				errCh <- fmt.Errorf("reader saw malformed JSON: %w", err)
				return
			}
		}
	}()

	for i := 0; i < 200; i++ {
		require.NoError(t, s.SetLabel("/tmp", []string{"cmd"}, fmt.Sprintf("v%d", i)))
	}
	close(stop)
	for err := range errCh {
		require.NoError(t, err)
	}
}

func TestMultipleKeysDoNotInterfere(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	require.NoError(t, err)

	require.NoError(t, s.SetLabel("/a", []string{"cmd1"}, "one"))
	require.NoError(t, s.SetLabel("/b", []string{"cmd2"}, "two"))

	v, err := s.Label("/a", []string{"cmd1"})
	require.NoError(t, err)
	require.Equal(t, "one", v)
	v, err = s.Label("/b", []string{"cmd2"})
	require.NoError(t, err)
	require.Equal(t, "two", v)

	// Insert remembered entries against each key; per-key caps are
	// enforced independently.
	for i := 0; i < 10; i++ {
		require.NoError(t, s.Remember(store.Remembered{Port: 40100 + i, Command: []string{"cmd1"}, Cwd: "/a"}))
		require.NoError(t, s.Remember(store.Remembered{Port: 56100 + i, Command: []string{"cmd2"}, Cwd: "/b"}))
	}

	a, err := s.ListRemembered("/a", []string{"cmd1"})
	require.NoError(t, err)
	require.Equal(t, store.PerKeyRememberedCap, len(a))

	b, err := s.ListRemembered("/b", []string{"cmd2"})
	require.NoError(t, err)
	require.Equal(t, store.PerKeyRememberedCap, len(b))
}
