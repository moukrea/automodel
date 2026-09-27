package ledger

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestStatesBounded(t *testing.T) {
	s := States{Dir: t.TempDir()}
	big := strings.Repeat("x", 40<<10)
	for i := 0; i < 80; i++ {
		if err := s.Put(StateRecord{ID: NewID(), TS: time.Now(), SessionID: "s1", State: map[string]any{"task": big}}); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := os.Stat(s.path("s1"))
	if st.Size() > maxStateFile {
		t.Errorf("file size %d", st.Size())
	}
	id := NewID()
	s.Put(StateRecord{ID: id, SessionID: "s1", State: map[string]any{"task": "hi"}})
	if r, err := s.Get("s1", id); err != nil || r.State["task"] != "hi" {
		t.Errorf("get: %v %v", r, err)
	}
	s.Put(StateRecord{ID: "huge", SessionID: "s1", State: map[string]any{"task": strings.Repeat("x", maxStateRecord)}})
	if _, err := s.Get("s1", "huge"); err != ErrNoState {
		t.Errorf("oversized record kept: %v", err)
	}
	if err := (States{}).Put(StateRecord{ID: "x", SessionID: "s1"}); err != nil {
		t.Error("opt-out store must be a no-op")
	}
}

func TestNth(t *testing.T) {
	ds := []Decision{
		{SessionID: "a", Scope: "main", ID: "1"}, {SessionID: "a", Scope: "subagent", ID: "2"},
		{SessionID: "a", Scope: "main", ID: "3"}, {SessionID: "a", Scope: "subagent", ID: "4"},
	}
	if d, _ := Nth(ds, "", "main", 1); d.ID != "3" {
		t.Errorf("latest main = %s", d.ID)
	}
	if d, _ := Nth(ds, "a", "main", 2); d.ID != "1" {
		t.Errorf("2nd main = %s", d.ID)
	}
	if _, ok := Nth(ds, "", "main", 3); ok {
		t.Error("3rd main exists?")
	}
}
