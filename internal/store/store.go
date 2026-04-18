// Package store persists port-manager's labels and remembered-listener
// entries to a single JSON file under ~/.port-manager/state.json.
//
// All writes are serialised behind a sync.Mutex and land on disk via an
// atomic write (tmp + fsync + rename) so a crash mid-write cannot corrupt
// the committed file. Stale .tmp artefacts from a prior crash are ignored
// at startup — hydration reads only state.json.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Public constants documenting the retention and size invariants.
const (
	// MaxLabelLen caps SetLabel input length (spec §5 "label length 0–64").
	MaxLabelLen = 64
	// PerKeyRememberedCap is the retention policy per (cwd, command) key.
	PerKeyRememberedCap = 5
	// GlobalRememberedCap is the hard ceiling on remembered entries
	// across all keys; oldest entries are evicted on overflow.
	GlobalRememberedCap = 100
	// MaxStateBytes enforces Iron Law 13 (state ≤ 256 KiB). If serialised
	// state exceeds this after per-key/global caps, remembered entries
	// are evicted oldest-first, then label entries oldest-first by
	// UpdatedAt, until it fits.
	MaxStateBytes = 256 * 1024

	stateVersion = 1
	stateDirName = ".port-manager"
	stateFile    = "state.json"
	tmpSuffix    = ".tmp"
)

// ErrLabelTooLong is returned by SetLabel when the label exceeds
// MaxLabelLen characters.
var ErrLabelTooLong = errors.New("store: label exceeds 64 characters")

// ErrStateSizeExceeded is returned by persist when serialised state
// exceeds MaxStateBytes and there are no remembered or label entries
// left to evict. It is a defensive guard — under normal operation
// eviction always finds a candidate.
var ErrStateSizeExceeded = errors.New("store: serialised state exceeds MaxStateBytes with nothing left to evict")

// Remembered is a single killed-but-restart-capable listener snapshot.
// The zero value of a Remembered passed to Remember is filled in (ID,
// KilledAt, LabelKey) before being persisted.
type Remembered struct {
	ID       string    `json:"id"`
	Port     int       `json:"port"`
	Command  []string  `json:"command"`
	Cwd      string    `json:"cwd"`
	Env      []string  `json:"env,omitempty"`
	KilledAt time.Time `json:"killed_at"`
	LabelKey string    `json:"label_key"`
}

type labelEntry struct {
	Cwd       string    `json:"cwd"`
	Command   []string  `json:"command"`
	Label     string    `json:"label"`
	UpdatedAt time.Time `json:"updated_at"`
}

type persisted struct {
	Version    int                   `json:"version"`
	Labels     map[string]labelEntry `json:"labels"`
	Remembered []Remembered          `json:"remembered"`
}

// Store is the concurrency-safe handle to the on-disk JSON state file.
// All public methods acquire an internal mutex; callers may share a
// single *Store across goroutines.
type Store struct {
	mu   sync.Mutex
	path string
	data persisted
}

// Open reads (or creates) the state file under homeDir/.port-manager/.
// If the file is missing, an empty state is initialised in memory and
// written on the next successful mutation. Stray .tmp files from a
// prior crashed write are left untouched — only state.json is read.
func Open(homeDir string) (*Store, error) {
	if homeDir == "" {
		return nil, errors.New("store: homeDir must not be empty")
	}
	dir := filepath.Join(homeDir, stateDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir state dir: %w", err)
	}
	s := &Store{
		path: filepath.Join(dir, stateFile),
		data: persisted{
			Version:    stateVersion,
			Labels:     map[string]labelEntry{},
			Remembered: []Remembered{},
		},
	}
	if err := s.hydrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) hydrate() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: read state: %w", err)
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("store: parse state: %w", err)
	}
	if p.Version == 0 {
		p.Version = stateVersion
	}
	if p.Labels == nil {
		p.Labels = map[string]labelEntry{}
	}
	if p.Remembered == nil {
		p.Remembered = []Remembered{}
	}
	s.data = p
	return nil
}

// LabelKey returns sha256(cwd \x00 argv[0] \x00 argv[1] …) truncated to
// 16 hex chars — the stable identifier the store uses to group labels
// and remembered entries that share the same working directory + argv.
// The null-byte separator between args prevents collision between e.g.
// ["ls -l"] and ["ls", "-l"].
func LabelKey(cwd string, command []string) string {
	h := sha256.New()
	h.Write([]byte(cwd))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(command, "\x00")))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:16]
}

// Label returns the label persisted for the given (cwd, command) pair.
// An empty string with a nil error means no label is set.
func (s *Store) Label(cwd string, command []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.data.Labels[LabelKey(cwd, command)]
	if !ok {
		return "", nil
	}
	return entry.Label, nil
}

// SetLabel upserts the label for (cwd, command). An empty label clears
// the stored entry. Labels with more than MaxLabelLen runes are
// rejected with ErrLabelTooLong before any I/O. Rune-counting (rather
// than byte length) matches the handler layer's validateLabel so both
// agree on a single "chars" semantic for multibyte input.
func (s *Store) SetLabel(cwd string, command []string, label string) error {
	if utf8.RuneCountInString(label) > MaxLabelLen {
		return ErrLabelTooLong
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := LabelKey(cwd, command)
	if label == "" {
		delete(s.data.Labels, key)
	} else {
		s.data.Labels[key] = labelEntry{
			Cwd:       cwd,
			Command:   append([]string(nil), command...),
			Label:     label,
			UpdatedAt: time.Now().UTC(),
		}
	}
	return s.persistLocked()
}

// Remember appends a killed-listener snapshot to the remembered list,
// deriving ID / LabelKey / KilledAt if the caller left them zero. The
// per-key cap (PerKeyRememberedCap) and global cap (GlobalRememberedCap)
// are both enforced atomically, dropping the oldest entries on overflow.
func (s *Store) Remember(r Remembered) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.LabelKey == "" {
		r.LabelKey = LabelKey(r.Cwd, r.Command)
	}
	if r.KilledAt.IsZero() {
		r.KilledAt = time.Now().UTC()
	}
	if r.ID == "" {
		id, err := newULID(r.KilledAt)
		if err != nil {
			return fmt.Errorf("store: ulid: %w", err)
		}
		r.ID = id
	}
	// Defensively copy slice fields so future caller mutations don't
	// rewrite store-owned backing arrays.
	r.Command = append([]string(nil), r.Command...)
	if r.Env != nil {
		r.Env = append([]string(nil), r.Env...)
	}
	s.data.Remembered = append(s.data.Remembered, r)
	s.evictPerKeyLocked(r.LabelKey)
	s.evictGlobalLocked()
	return s.persistLocked()
}

// ListRemembered returns remembered entries for (cwd, command), newest
// first. The returned slice is a copy with deep-copied Env/Command
// slices; callers may mutate it freely.
func (s *Store) ListRemembered(cwd string, command []string) ([]Remembered, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := LabelKey(cwd, command)
	out := make([]Remembered, 0, PerKeyRememberedCap)
	for i := len(s.data.Remembered) - 1; i >= 0; i-- {
		if s.data.Remembered[i].LabelKey == key {
			out = append(out, cloneRemembered(s.data.Remembered[i]))
		}
	}
	return out, nil
}

// AllRemembered returns every persisted remembered entry, newest
// first. Each entry is deep-copied so callers may mutate the result
// without racing the store. Used by the dashboard's "restart history"
// view which is not keyed on a specific (cwd, command) pair.
func (s *Store) AllRemembered() ([]Remembered, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Remembered, 0, len(s.data.Remembered))
	for i := len(s.data.Remembered) - 1; i >= 0; i-- {
		out = append(out, cloneRemembered(s.data.Remembered[i]))
	}
	return out, nil
}

// FindRemembered returns the remembered entry matching id, or (nil, nil)
// when none exists. The returned entry has deep-copied Env/Command
// slices; callers may mutate it freely.
func (s *Store) FindRemembered(id string) (*Remembered, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Remembered {
		if s.data.Remembered[i].ID == id {
			r := cloneRemembered(s.data.Remembered[i])
			return &r, nil
		}
	}
	return nil, nil
}

// DeleteRemembered removes the entry with the given id. Missing ids are
// silently ignored so callers can treat it as idempotent.
func (s *Store) DeleteRemembered(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]Remembered, 0, len(s.data.Remembered))
	changed := false
	for _, r := range s.data.Remembered {
		if r.ID == id {
			changed = true
			continue
		}
		kept = append(kept, r)
	}
	if !changed {
		return nil
	}
	s.data.Remembered = kept
	return s.persistLocked()
}

func cloneRemembered(r Remembered) Remembered {
	out := r
	if r.Command != nil {
		out.Command = append([]string(nil), r.Command...)
	}
	if r.Env != nil {
		out.Env = append([]string(nil), r.Env...)
	}
	return out
}

// evictPerKeyLocked trims entries for key to the most-recent
// PerKeyRememberedCap, preserving insertion order of everything else.
func (s *Store) evictPerKeyLocked(key string) {
	matches := 0
	for _, r := range s.data.Remembered {
		if r.LabelKey == key {
			matches++
		}
	}
	if matches <= PerKeyRememberedCap {
		return
	}
	toDrop := matches - PerKeyRememberedCap
	kept := make([]Remembered, 0, len(s.data.Remembered)-toDrop)
	for _, r := range s.data.Remembered {
		if r.LabelKey == key && toDrop > 0 {
			toDrop--
			continue
		}
		kept = append(kept, r)
	}
	s.data.Remembered = kept
}

// evictGlobalLocked drops the oldest entries until the list fits under
// GlobalRememberedCap.
func (s *Store) evictGlobalLocked() {
	if len(s.data.Remembered) <= GlobalRememberedCap {
		return
	}
	excess := len(s.data.Remembered) - GlobalRememberedCap
	s.data.Remembered = append([]Remembered(nil), s.data.Remembered[excess:]...)
}

// evictOldestLabelLocked removes the single label entry with the oldest
// UpdatedAt. Returns true if an entry was removed.
func (s *Store) evictOldestLabelLocked() bool {
	if len(s.data.Labels) == 0 {
		return false
	}
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range s.data.Labels {
		if first || e.UpdatedAt.Before(oldestAt) {
			oldestKey = k
			oldestAt = e.UpdatedAt
			first = false
		}
	}
	delete(s.data.Labels, oldestKey)
	return true
}

// persistLocked serialises the in-memory state and atomically writes it
// to disk. Callers MUST hold s.mu. Iron Law 13: if the serialised state
// exceeds MaxStateBytes, remembered entries are evicted oldest-first;
// once they're exhausted, label entries are evicted oldest-first by
// UpdatedAt. If both are empty and the state still exceeds the cap,
// ErrStateSizeExceeded is surfaced.
func (s *Store) persistLocked() error {
	for {
		buf, err := json.MarshalIndent(s.data, "", "  ")
		if err != nil {
			return fmt.Errorf("store: marshal: %w", err)
		}
		if len(buf) <= MaxStateBytes {
			return writeAtomic(s.path, buf)
		}
		if len(s.data.Remembered) > 0 {
			s.data.Remembered = append([]Remembered(nil), s.data.Remembered[1:]...)
			continue
		}
		if s.evictOldestLabelLocked() {
			continue
		}
		return ErrStateSizeExceeded
	}
}

// writeAtomic writes data to path.tmp, fsyncs, then renames over path.
// On any intermediate failure the tmp file is best-effort removed so the
// next Open sees only a clean state.json.
func writeAtomic(path string, data []byte) (retErr error) {
	tmp := path + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("store: open tmp: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	return nil
}

// newULID returns a 26-char Crockford-base32 ULID built from a 48-bit
// millisecond timestamp (from ts) and 80 bits of cryptographic randomness.
// Two ULIDs generated in the same millisecond remain unique and are
// lexicographically sortable in time order.
func newULID(ts time.Time) (string, error) {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var raw [16]byte
	ms := uint64(ts.UnixMilli())
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", err
	}
	var out [26]byte
	out[0] = crockford[(raw[0]&0xE0)>>5]
	out[1] = crockford[raw[0]&0x1F]
	out[2] = crockford[(raw[1]&0xF8)>>3]
	out[3] = crockford[((raw[1]&0x07)<<2)|((raw[2]&0xC0)>>6)]
	out[4] = crockford[(raw[2]&0x3E)>>1]
	out[5] = crockford[((raw[2]&0x01)<<4)|((raw[3]&0xF0)>>4)]
	out[6] = crockford[((raw[3]&0x0F)<<1)|((raw[4]&0x80)>>7)]
	out[7] = crockford[(raw[4]&0x7C)>>2]
	out[8] = crockford[((raw[4]&0x03)<<3)|((raw[5]&0xE0)>>5)]
	out[9] = crockford[raw[5]&0x1F]
	out[10] = crockford[(raw[6]&0xF8)>>3]
	out[11] = crockford[((raw[6]&0x07)<<2)|((raw[7]&0xC0)>>6)]
	out[12] = crockford[(raw[7]&0x3E)>>1]
	out[13] = crockford[((raw[7]&0x01)<<4)|((raw[8]&0xF0)>>4)]
	out[14] = crockford[((raw[8]&0x0F)<<1)|((raw[9]&0x80)>>7)]
	out[15] = crockford[(raw[9]&0x7C)>>2]
	out[16] = crockford[((raw[9]&0x03)<<3)|((raw[10]&0xE0)>>5)]
	out[17] = crockford[raw[10]&0x1F]
	out[18] = crockford[(raw[11]&0xF8)>>3]
	out[19] = crockford[((raw[11]&0x07)<<2)|((raw[12]&0xC0)>>6)]
	out[20] = crockford[(raw[12]&0x3E)>>1]
	out[21] = crockford[((raw[12]&0x01)<<4)|((raw[13]&0xF0)>>4)]
	out[22] = crockford[((raw[13]&0x0F)<<1)|((raw[14]&0x80)>>7)]
	out[23] = crockford[(raw[14]&0x7C)>>2]
	out[24] = crockford[((raw[14]&0x03)<<3)|((raw[15]&0xE0)>>5)]
	out[25] = crockford[raw[15]&0x1F]
	return string(out[:]), nil
}
