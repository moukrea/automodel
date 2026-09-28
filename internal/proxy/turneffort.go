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

// turnEffort rewrites fields for per-turn effort and returns the top-level
// effort to send. want is the effort of the current decision. readOnly
// (count_tokens) applies the recorded marks without binding or resetting.
func (p *Proxy) turnEffort(sessionID string, fields map[string]json.RawMessage, want string, readOnly bool) (string, bool) {
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

	var base string
	var marks []state.EffortMark
	apply := func(s *state.Session) bool {
		changed := false
		if s.PerTurnRejected {
			base = ""
			return false
		}
		if s.EffortBase == "" {
			s.EffortBase, s.EffortMarks, changed = want, nil, true
		}
		for _, mk := range s.EffortMarks {
			if mk.Index >= len(kept) || anchor(kept[mk.Index]) != mk.Anchor {
				// History rewritten (compaction, rewind): the cache is gone
				// anyway, start a new epoch at the current effort.
				log.Printf("per-turn effort %s: history changed at message %d, new epoch at %s", short(sessionID), mk.Index, want)
				s.EffortBase, s.EffortMarks, s.PendingEffort, changed = want, nil, nil, true
				break
			}
		}
		// The conversation always opens with an explicit statement: with the
		// beta on, an unstated effort is rendered at the newest turn, which
		// moves on every prompt and would defeat the cache (Claude Code does
		// the same).
		if len(s.EffortMarks) == 0 && !readOnly {
			if i := firstUser(msgs); i >= 0 {
				s.EffortMarks, changed = []state.EffortMark{{Index: i, Anchor: anchor(kept[i]), Effort: s.EffortBase}}, true
			}
		}
		if pe := s.PendingEffort; pe != nil && !readOnly {
			if i := promptIndex(msgs); i >= 0 {
				effective := s.EffortBase
				var keep []state.EffortMark
				for _, mk := range s.EffortMarks {
					if mk.Index < i {
						effective = mk.Effort
						keep = append(keep, mk)
					}
				}
				if pe.Effort != effective {
					keep = append(keep, state.EffortMark{Index: i, Anchor: anchor(kept[i]), Effort: pe.Effort})
				}
				s.EffortMarks, s.PendingEffort, changed = keep, nil, true
			}
		}
		base, marks = s.EffortBase, append([]state.EffortMark(nil), s.EffortMarks...)
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
	out := make([]json.RawMessage, 0, len(kept)+len(marks))
	next := 0
	for i, r := range kept {
		out = append(out, r)
		for next < len(marks) && marks[next].Index == i {
			sys, _ := json.Marshal(map[string]any{
				"role": "system", "content": []any{},
				"output_config": map[string]string{"effort": marks[next].Effort},
			})
			out = append(out, sys)
			next++
		}
	}
	fields["messages"], _ = json.Marshal(out)
	return base, true
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
	if !strings.Contains(low, "output_config") && !strings.Contains(low, "system") && !strings.Contains(low, "per-turn") && !strings.Contains(low, "per_turn") {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	log.Printf("per-turn effort refused for %s (%s); retrying without it", short(rt.sessionID), strings.TrimSpace(string(body)))
	if _, err := t.p.State.Update(rt.sessionID, func(s *state.Session) bool {
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
