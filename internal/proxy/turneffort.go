package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/state"
)

// Per-turn effort. Changing the top-level output_config.effort between two
// requests invalidates the prompt cache (the effort is rendered into the
// prompt). Models with per_turn_effort accept an effort-only system message
// instead, which applies from the next user turn and leaves everything before
// it untouched. The proxy keeps the top-level effort fixed for the whole
// conversation (Session.EffortBase) and re-inserts the same system messages
// at the same places on every request, so the prefix stays byte-identical.

// msg is the part of a message the proxy inspects.
type msg struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	OutputConfig json.RawMessage `json:"output_config"`
}

// effortOnly reports whether m is an effort-only system message (Claude
// Code's own per-turn effort, or ours from an earlier pass). They are
// stripped so ours are the only ones, placed deterministically.
func effortOnly(m msg) bool {
	if m.Role != "system" || len(m.OutputConfig) == 0 {
		return false
	}
	c := bytes.TrimSpace(m.Content)
	return len(c) == 0 || bytes.Equal(c, []byte("[]")) || bytes.Equal(c, []byte(`""`))
}

// anchor hashes what a message says, for the model: its role, and each
// content block's type, text, tool name, input, id and result, in order.
// Claude Code moves cache_control markers from request to request, and
// newer versions touch other fields of a message that aren't rendered: a
// hash over them saw the first message "change" on every request (a new
// effort epoch each time, and a top-level effort change at the next
// switch, which rewrites the cache).
func anchor(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprint(m["role"]))
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			b.WriteString("\x00s:" + x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			b.WriteString("\x00t:" + fmt.Sprint(x["type"]))
			for _, k := range []string{"text", "name", "id", "tool_use_id", "thinking", "data", "signature"} {
				if s, ok := x[k].(string); ok {
					b.WriteString("\x00" + k + ":" + s)
				}
			}
			if in, ok := x["input"]; ok {
				j, _ := json.Marshal(in) // map keys are sorted: deterministic
				b.WriteString("\x00input:" + string(j))
			}
			if c, ok := x["content"]; ok {
				walk(c)
			}
			if src, ok := x["source"].(map[string]any); ok {
				b.WriteString("\x00src:" + fmt.Sprint(src["type"], src["media_type"], len(fmt.Sprint(src["data"]))))
			}
		}
	}
	walk(m["content"])
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:12])
}

// promptIndex returns the index of the newest user message. A pending
// change is bound on the first request after the prompt was submitted, when
// that message is the prompt itself.
func promptIndex(msgs []msg) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}

func firstUser(msgs []msg) int {
	for i, m := range msgs {
		if m.Role == "user" {
			return i
		}
	}
	return -1
}

// effortTrack points at one conversation's per-turn effort state: the main
// thread's (Session.EffortBase...) or a subagent's (Session.AgentEfforts).
type effortTrack struct {
	base     *string
	marks    *[]state.EffortMark
	pending  **state.PendingEffort
	rejected *bool
}

// mainTrack is the main thread's per-turn effort state.
func mainTrack(s *state.Session) (*effortTrack, bool) {
	return &effortTrack{&s.EffortBase, &s.EffortMarks, &s.PendingEffort, &s.PerTurnRejected}, false
}

// agentTrack is a subagent's per-turn effort state, created on its first
// request. Its effort is always "pending": a request whose newest message
// comes after a decision that changed the effort (a message sent to the
// agent) gets a mark there.
func agentTrack(agentID, model, want string, now time.Time) func(s *state.Session) (*effortTrack, bool) {
	return func(s *state.Session) (*effortTrack, bool) {
		changed := false
		ae := s.AgentEfforts[agentID]
		if ae == nil || ae.Model != model {
			if s.AgentEfforts == nil {
				s.AgentEfforts = map[string]*state.AgentEffort{}
			}
			for id, old := range s.AgentEfforts {
				if now.Sub(old.UsedAt) > agentEffortTTL {
					delete(s.AgentEfforts, id)
				}
			}
			ae = &state.AgentEffort{Model: model}
			s.AgentEfforts[agentID], changed = ae, true
		}
		if now.Sub(ae.UsedAt) > time.Hour {
			ae.UsedAt, changed = now, true
		}
		pe := &state.PendingEffort{Effort: want}
		return &effortTrack{&ae.Base, &ae.Marks, &pe, &ae.Rejected}, changed
	}
}

// agentEffortTTL drops the per-turn state of agents idle this long.
const agentEffortTTL = 48 * time.Hour

// turnEffort rewrites fields for per-turn effort and returns the top-level
// effort to send. want is the effort of the current decision. readOnly
// (count_tokens) applies the recorded marks without binding or resetting.
func (p *Proxy) turnEffort(sessionID string, fields map[string]json.RawMessage, want string, readOnly bool) (string, bool) {
	return p.perTurnEffort(sessionID, fields, want, readOnly, mainTrack)
}

// perTurnEffort is turnEffort for the conversation pick returns.
func (p *Proxy) perTurnEffort(sessionID string, fields map[string]json.RawMessage, want string, readOnly bool, pick func(*state.Session) (*effortTrack, bool)) (string, bool) {
	var raws []json.RawMessage
	if json.Unmarshal(fields["messages"], &raws) != nil || len(raws) == 0 || sessionID == "" {
		return "", false
	}
	var kept []json.RawMessage
	var msgs []msg
	for _, r := range raws {
		var m msg
		if json.Unmarshal(r, &m) != nil {
			return "", false
		}
		if effortOnly(m) {
			continue
		}
		kept, msgs = append(kept, r), append(msgs, m)
	}

	// Claude Code 2.1.284 may send a conversation as a message thread: a
	// "create" request carries the whole history, a "continue" request only
	// what follows the thread's last message (the rest lives server side),
	// and output_config must stay the same across the thread. On a continue
	// the earlier marks are already in the thread: only a new one, after
	// the new prompt, is added, and the top-level effort never moves.
	var th struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(fields["thread"], &th)
	continuing := th.Type == "continue"

	var base string
	var marks []state.EffortMark
	apply := func(s *state.Session) bool {
		t, changed := pick(s)
		if *t.rejected {
			base = ""
			return changed
		}
		if *t.base == "" {
			*t.base, *t.marks, changed = want, nil, true
			if continuing {
				// A thread we didn't see start: keep the effort it was created with.
				if e := clientEffort(fields); e != "" {
					*t.base = e
				}
			}
		}
		if continuing {
			for k := range *t.marks {
				(*t.marks)[k].Index = -1 // in the thread already, not in this request
			}
		} else {
			// Place each mark after the message it follows, found by what that
			// message says (in order); a missing one means the history was
			// rewritten (compaction, rewind): the cache is gone anyway, start a
			// new epoch at the current effort.
			pos := 0
			for k, mk := range *t.marks {
				j := pos
				for j < len(kept) && anchor(kept[j]) != mk.Anchor {
					j++
				}
				if j == len(kept) {
					log.Printf("per-turn effort %s: history changed (mark %d of %d not found in %d messages, thread %q), new epoch at %s", short(sessionID), k, len(*t.marks), len(kept), th.Type, want)
					*t.base, *t.marks, *t.pending, changed = want, nil, nil, true
					break
				}
				if (*t.marks)[k].Index != j {
					(*t.marks)[k].Index, changed = j, true
				}
				pos = j + 1
			}
			// The conversation always opens with an explicit statement: with the
			// beta on, an unstated effort is rendered at the newest turn, which
			// moves on every prompt and would defeat the cache (Claude Code does
			// the same).
			if len(*t.marks) == 0 && !readOnly {
				if i := firstUser(msgs); i >= 0 {
					*t.marks, changed = []state.EffortMark{{Index: i, Anchor: anchor(kept[i]), Effort: *t.base}}, true
				}
			}
		}
		if pe := *t.pending; pe != nil && !readOnly {
			if i := promptIndex(msgs); i >= 0 {
				effective := *t.base
				var keep []state.EffortMark
				placed := false
				for _, mk := range *t.marks {
					switch {
					case continuing || mk.Index < i:
						effective = mk.Effort
						keep = append(keep, mk)
					case mk.Index == i && mk.Effort == pe.Effort:
						// Already there (the opening statement, or this request
						// sent again): keep it where it is.
						keep, placed = append(keep, mk), true
					}
				}
				if !placed && pe.Effort != effective {
					keep = append(keep, state.EffortMark{Index: i, Anchor: anchor(kept[i]), Effort: pe.Effort})
				}
				if len(keep) != len(*t.marks) || pe.Effort != effective {
					changed = true
				}
				*t.marks, *t.pending = keep, nil
				if t.pending == &s.PendingEffort {
					changed = true // the main thread's pending change is now bound
				}
			}
		}
		base, marks = *t.base, append([]state.EffortMark(nil), *t.marks...)
		return changed
	}
	if readOnly {
		sess, err := p.State.Load(sessionID)
		if err != nil {
			return "", false
		}
		apply(sess)
	} else if _, err := p.State.Update(sessionID, apply); err != nil {
		log.Printf("per-turn effort %s: %v", short(sessionID), err)
		return "", false
	}
	if base == "" {
		return "", false
	}

	// Each statement follows the user message it applies to, before the
	// response (Claude Code's placement); the newest one may end the list.
	// A system message of Claude Code's own that states an effort (it puts
	// medium on the first one of every subagent conversation) says the
	// effort in force at its place instead, or it would override ours.
	out := make([]json.RawMessage, 0, len(kept)+len(marks))
	effective := base
	for i, r := range kept {
		if msgs[i].Role == "system" && len(msgs[i].OutputConfig) > 0 {
			r = restate(r, effective)
		}
		out = append(out, r)
		for _, mk := range marks {
			if mk.Index == i {
				sys, _ := json.Marshal(map[string]any{
					"role": "system", "content": []any{},
					"output_config": map[string]string{"effort": mk.Effort},
				})
				out = append(out, sys)
				effective = mk.Effort
			}
		}
	}
	fields["messages"], _ = json.Marshal(out)
	return base, true
}

// restate sets the effort a system message states, keeping the rest of it.
func restate(raw json.RawMessage, effort string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	var oc map[string]json.RawMessage
	if json.Unmarshal(m["output_config"], &oc) != nil {
		return raw
	}
	var cur string
	if json.Unmarshal(oc["effort"], &cur) != nil || cur == "" || cur == effort {
		return raw
	}
	oc["effort"], _ = json.Marshal(effort)
	m["output_config"], _ = json.Marshal(oc)
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// retryTransport resends a request without per-turn effort when the API
// refuses it, and turns the mechanism off for the session: the user never
// sees the error.
type retryTransport struct {
	base http.RoundTripper
	p    *Proxy
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	rt, _ := req.Context().Value(ctxKey{}).(*route)
	if err != nil || resp.StatusCode != http.StatusBadRequest || rt == nil || rt.plainBody == nil {
		return resp, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	low := strings.ToLower(string(body))
	// A thread fingerprint mismatch isn't a refusal of per-turn effort:
	// Claude Code recovers from it by starting a new thread.
	if strings.Contains(low, "thread_fingerprint_mismatch") || (!strings.Contains(low, "output_config") && !strings.Contains(low, "system") && !strings.Contains(low, "per-turn") && !strings.Contains(low, "per_turn")) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	log.Printf("per-turn effort refused for %s (%s); retrying without it", short(rt.sessionID), strings.TrimSpace(string(body)))
	if _, err := t.p.State.Update(rt.sessionID, func(s *state.Session) bool {
		if rt.agentEffort {
			if ae := s.AgentEfforts[rt.agentID]; ae != nil {
				ae.Rejected, ae.Base, ae.Marks = true, "", nil
			}
			return true
		}
		s.PerTurnRejected, s.EffortBase, s.EffortMarks, s.PendingEffort = true, "", nil, nil
		return true
	}); err != nil {
		log.Printf("state %s: %v", short(rt.sessionID), err)
	}
	r2 := req.Clone(req.Context())
	r2.Body = io.NopCloser(bytes.NewReader(rt.plainBody))
	r2.ContentLength = int64(len(rt.plainBody))
	r2.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(rt.plainBody)), nil }
	setBeta(r2.Header, rt.perTurnBeta, false)
	return t.base.RoundTrip(r2)
}

// perTurn reports whether per-turn effort applies to m in this catalog.
func perTurn(cat *catalog.Catalog, m *catalog.Model) bool {
	return m != nil && m.PerTurnEffort && len(m.Efforts) > 0 && cat.Meta.PerTurnEffortBeta != ""
}
