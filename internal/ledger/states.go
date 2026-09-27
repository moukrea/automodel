package ledger

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/moukrea/automodel/internal/flock"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// States keeps, per session, the routing state each decision sent to Jev,
// so `automodel flag` can turn a decision into an eval case. It stays
// local (one JSONL file per session under Dir) and is size-bounded.
type States struct{ Dir string }

// StateRecord is one routing state, keyed by the decision's ID.
type StateRecord struct {
	ID        string         `json:"id"`
	TS        time.Time      `json:"ts"`
	SessionID string         `json:"session_id"`
	Scope     string         `json:"scope"`
	Warm      bool           `json:"warm,omitempty"`
	State     map[string]any `json:"state"`
}

const (
	maxStateRecord = 128 << 10 // a larger state is not kept
	maxStateFile   = 2 << 20   // per session; the oldest half goes beyond
)

var safeSession = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// NewID returns a random decision ID.
func NewID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s States) path(session string) string {
	return filepath.Join(s.Dir, "states", session+".jsonl")
}

// Put appends a record. Records too large, or without an ID or a valid
// session, are dropped.
func (s States) Put(r StateRecord) error {
	if s.Dir == "" || r.ID == "" || !safeSession.MatchString(r.SessionID) {
		return nil
	}
	line, err := json.Marshal(r)
	if err != nil || len(line) > maxStateRecord {
		return err
	}
	p := s.path(r.SessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	unlock, err := flock.Lock(f)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil && st.Size() > maxStateFile {
		return trimHalf(f, p)
	}
	return nil
}

// trimHalf keeps the newest half of the file's lines (under the caller's lock).
func trimHalf(f *os.File, p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	cut := bytes.IndexByte(b[len(b)/2:], '\n')
	if cut < 0 {
		return nil
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err = f.Write(b[len(b)/2+cut+1:])
	return err
}

// ErrNoState is returned when no routing state was kept for a decision.
var ErrNoState = errors.New("no routing state kept for that decision")

// Get returns the state recorded for a decision ID.
func (s States) Get(session, id string) (*StateRecord, error) {
	if !safeSession.MatchString(session) || id == "" {
		return nil, ErrNoState
	}
	f, err := os.Open(s.path(session))
	if os.IsNotExist(err) {
		return nil, ErrNoState
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 2*maxStateRecord)
	key := []byte(`"id":"` + id + `"`)
	for sc.Scan() {
		if !bytes.Contains(sc.Bytes(), key) {
			continue
		}
		var r StateRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID == id {
			return &r, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, ErrNoState
}
