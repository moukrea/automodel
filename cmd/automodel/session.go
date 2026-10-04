package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
)

const sessionUsage = `usage: automodel session [--session id] <action>
  show [--json]              the decision in force, the work in progress and the paused work
  resume [--keep-detour]     bring the paused work back as the work in progress
                             (the work in progress is dropped, or waits as a kept detour)
  work <level> [--mode m|off]  set the work in progress's level (an effort or a tier ID), keeping its goal
The change is made under the session's lock and applies from its next prompt.`

// sessionCmd shows or corrects a live session's routing state under its
// lock: bring back paused work, set the work's level.
func sessionCmd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("session", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, sessionUsage) }
	session := fs.String("session", "", "session ID or prefix (default: the most recently updated)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	store := state.Store{Dir: cfg.StateDir}
	sid, err := findSession(cfg.StateDir, *session)
	if err != nil {
		return err
	}
	action, rest := fs.Arg(0), fs.Args()[1:]
	switch action {
	case "show":
		afs := flag.NewFlagSet("show", flag.ExitOnError)
		jsonOut := afs.Bool("json", false, "the session's state as JSON")
		afs.Parse(rest)
		s, err := store.Load(sid)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"session_id": sid, "main": s.Main, "work": s.Work, "paused": s.Paused})
		}
		printSession(s, time.Now())
		return nil
	case "resume":
		afs := flag.NewFlagSet("resume", flag.ExitOnError)
		keep := afs.Bool("keep-detour", false, "the work in progress waits as a kept detour (closed by the next wrap-up)")
		afs.Parse(rest)
		var nothing bool
		s, err := store.Update(sid, func(s *state.Session) bool {
			if s.Paused == nil {
				nothing = true
				return false
			}
			p := s.Paused
			u := &router.WorkUpdate{Kind: router.WorkResumed, Tier: p.Tier, Mode: p.Mode, Model: p.Model, Pause: *keep}
			u.Apply(s, "", time.Now())
			return true
		})
		if err != nil {
			return err
		}
		if nothing {
			return fmt.Errorf("session %s has no paused work", short(sid))
		}
		printSession(s, time.Now())
		return nil
	case "work":
		afs := flag.NewFlagSet("work", flag.ExitOnError)
		mode := afs.String("mode", "", "the work's mode (a catalog mode, or off)")
		level, flags := "", rest
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			level, flags = rest[0], rest[1:]
		}
		afs.Parse(flags)
		if level == "" && afs.NArg() > 0 {
			level = afs.Arg(0)
		}
		if level == "" {
			return errors.New("work: a level is needed (an effort such as medium, or a tier ID)")
		}
		c, _, err := router.LoadCatalog(cfg)
		if err != nil {
			return err
		}
		if *mode != "" && *mode != "off" && c.Modes[*mode] == nil {
			return fmt.Errorf("work: unknown mode %q", *mode)
		}
		var bad error
		s, err := store.Update(sid, func(s *state.Session) bool {
			prev := s.WorkInProgress()
			model := c.DefaultTier(catalog.ScopeMain).Model
			switch {
			case prev != nil && prev.Model != "":
				model = prev.Model
			case s.Main != nil && s.Main.Model != "":
				model = s.Main.Model
			}
			t := c.Tier(catalog.ScopeMain, level)
			if t == nil {
				t = router.EffortTier(c, model, level)
			}
			if t == nil {
				bad = fmt.Errorf("work: no main tier for %q", level)
				return false
			}
			w := state.Work{Tier: t.ID, Since: time.Now()}
			if prev != nil {
				w = *prev
				w.Tier = t.ID
			}
			switch *mode {
			case "":
			case "off":
				w.Mode = ""
			default:
				w.Mode = *mode
			}
			if m := c.Modes[w.Mode]; m != nil {
				if min := c.Tier(catalog.ScopeMain, m.MinTier); min != nil && t.Rank < min.Rank {
					bad = fmt.Errorf("work: mode %s needs %s at least", w.Mode, min.ID)
					return false
				}
			}
			s.Work = &w
			return true
		})
		if err != nil {
			return err
		}
		if bad != nil {
			return bad
		}
		printSession(s, time.Now())
		return nil
	}
	fs.Usage()
	os.Exit(2)
	return nil
}

// findSession resolves a session ID or prefix among the recorded sessions
// ("": the most recently updated).
func findSession(stateDir, want string) (string, error) {
	files, _ := filepath.Glob(filepath.Join(stateDir, "sessions", "*.json"))
	type found struct {
		id  string
		mod time.Time
	}
	var all []found
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if want != "" && !strings.HasPrefix(id, want) {
			continue
		}
		if want != "" && id == want {
			return id, nil
		}
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		all = append(all, found{id, fi.ModTime()})
	}
	switch {
	case len(all) == 0 && want != "":
		return "", fmt.Errorf("no session %s", want)
	case len(all) == 0:
		return "", errors.New("no session recorded yet")
	case len(all) > 1 && want != "":
		return "", fmt.Errorf("%d sessions start with %s: give more of the ID", len(all), want)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	return all[0].id, nil
}

func printSession(s *state.Session, now time.Time) {
	fmt.Printf("session %s\n", s.SessionID)
	if d := s.Main; d != nil {
		fmt.Printf("  decision: %s (%s %s%s)\n", d.Tier, d.Model, d.Effort, plus(d.Mode))
	}
	if s.Pin != "" {
		fmt.Printf("  pinned:   %s (%s)\n", s.Pin, s.PinSource)
	}
	work := func(label string, w *state.Work) {
		if w == nil {
			fmt.Printf("  %s none\n", label)
			return
		}
		var tags []string
		if w.Done {
			tags = append(tags, "done")
		}
		if w.Kept {
			tags = append(tags, "kept detour")
		}
		if !w.Since.IsZero() {
			tags = append(tags, "since "+w.Since.Local().Format("15:04"))
		}
		goal := []rune(w.Goal)
		if len(goal) > 80 {
			goal = append(goal[:79], '…')
		}
		fmt.Printf("  %s %s%s [%s] %q\n", label, w.Tier, plus(w.Mode), strings.Join(tags, ", "), string(goal))
	}
	work("work:    ", s.Work)
	if p := s.Paused; p != nil && s.PausedWork(now) == nil {
		work("paused:  ", p)
		fmt.Printf("            (past the %s a paused work waits)\n", state.PausedTTL)
	} else {
		work("paused:  ", p)
	}
}

func plus(mode string) string {
	if mode == "" {
		return ""
	}
	return " +" + mode
}

func short(id string) string { return id[:min(8, len(id))] }
