package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Daily spend: the dollars of every response the proxy priced today (local
// day), persisted in <dir>/spend.json for the budget cap.

type daySpend struct {
	Day string  `json:"day"`
	USD float64 `json:"usd"`
}

func (s Store) spendPath() string { return filepath.Join(s.Dir, "spend.json") }

func day(t time.Time) string { return t.Local().Format("2006-01-02") }

// AddSpend adds usd to the day's total (a new day starts from zero).
func (s Store) AddSpend(now time.Time, usd float64) error {
	if usd <= 0 {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := lock(s.spendPath() + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	d := s.readSpend()
	if d.Day != day(now) {
		d = daySpend{Day: day(now)}
	}
	d.USD += usd
	b, _ := json.Marshal(d)
	tmp := s.spendPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.spendPath())
}

// SpentToday is the day's total so far.
func (s Store) SpentToday(now time.Time) float64 {
	if d := s.readSpend(); d.Day == day(now) {
		return d.USD
	}
	return 0
}

func (s Store) readSpend() daySpend {
	var d daySpend
	if b, err := os.ReadFile(s.spendPath()); err == nil {
		json.Unmarshal(b, &d)
	}
	return d
}
