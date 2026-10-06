package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/state"
)

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// modifyResponse taps /v1/messages responses to record token usage. The
// body is still streamed through untouched; parsing happens on the side.
func (p *Proxy) modifyResponse(resp *http.Response) error {
	rt, _ := resp.Request.Context().Value(ctxKey{}).(*route)
	if rt == nil || rt.model == "" || !strings.HasSuffix(rt.path, "/v1/messages") {
		return nil
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		if p.Debug {
			log.Printf("usage tap skipped: Content-Encoding %s", enc)
		}
		return nil
	}
	ct := resp.Header.Get("Content-Type")
	sse := strings.HasPrefix(ct, "text/event-stream")
	if !sse && !strings.HasPrefix(ct, "application/json") {
		return nil
	}
	if rt.asked != "" && resp.StatusCode < 300 {
		if sse {
			resp.Body = &servedAs{ReadCloser: resp.Body, to: rt.asked}
		} else if err := servedAsJSON(resp, rt.asked); err != nil {
			return err
		}
	}
	resp.Body = &tap{ReadCloser: resp.Body, p: p, rt: rt, status: resp.StatusCode, sse: sse}
	return nil
}

// servedAs names the custom model as the one that answered, in the
// message_start event of a routed stream (the model the request was
// routed to stays in the ledger). Claude Code records a response's model
// in the transcript and a resumed session restores the model of its last
// answer: the served one took the session off routing (live: every
// session resumed after a reboot ran unrouted on Opus until /model jev).
// Only the first event is buffered, line by line; the rest streams as is.
type servedAs struct {
	io.ReadCloser
	to   string
	done bool
	in   []byte // a partial line, before the first event
	out  []byte // bytes ready, not yet returned
	err  error  // the upstream error, returned once out is drained
}

func (s *servedAs) Read(b []byte) (int, error) {
	for !s.done && len(s.out) == 0 && s.err == nil {
		buf := make([]byte, 32<<10)
		n, err := s.ReadCloser.Read(buf)
		s.in = append(s.in, buf[:n]...)
		for !s.done {
			i := bytes.IndexByte(s.in, '\n')
			if i < 0 {
				break
			}
			line := s.in[:i+1]
			if bytes.HasPrefix(line, []byte("data:")) {
				line, s.done = renameModel(line, s.to), true
			}
			s.out = append(s.out, line...)
			s.in = s.in[i+1:]
		}
		if s.done || err != nil || len(s.in) > 1<<20 {
			s.out, s.in, s.done = append(s.out, s.in...), nil, true
		}
		s.err = err
	}
	if len(s.out) > 0 {
		n := copy(b, s.out)
		s.out = s.out[n:]
		return n, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return 0, err
	}
	return s.ReadCloser.Read(b)
}

var modelField = regexp.MustCompile(`"model"\s*:\s*"[^"]*"`)

// renameModel names the model in a message_start event's data line.
func renameModel(line []byte, to string) []byte {
	if !bytes.Contains(line, []byte(`"message_start"`)) {
		return line
	}
	done := false
	return modelField.ReplaceAllFunc(line, func(m []byte) []byte {
		if done {
			return m
		}
		done = true
		q, _ := json.Marshal(to)
		return append([]byte(`"model":`), q...)
	})
}

// servedAsJSON names the custom model in a whole (non-streamed) message.
func servedAsJSON(resp *http.Response, to string) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONTap+1))
	if err != nil {
		resp.Body.Close()
		return err
	}
	if len(body) <= maxJSONTap {
		var m map[string]json.RawMessage
		var model string
		if json.Unmarshal(body, &m) == nil && json.Unmarshal(m["model"], &model) == nil {
			m["model"], _ = json.Marshal(to)
			if nb, err := json.Marshal(m); err == nil {
				body = nb
			}
		}
	}
	rest := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), rest), rest}
	if len(body) <= maxJSONTap {
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	return nil
}

type tap struct {
	io.ReadCloser
	p      *Proxy
	rt     *route
	status int
	sse    bool

	line []byte       // partial SSE line
	buf  bytes.Buffer // JSON body (bounded)
	u    usage
	seen bool
	once sync.Once
}

const maxJSONTap = 4 << 20

func (t *tap) Read(b []byte) (int, error) {
	n, err := t.ReadCloser.Read(b)
	if n > 0 {
		if t.sse {
			t.scan(b[:n])
		} else if t.buf.Len() < maxJSONTap {
			t.buf.Write(b[:n])
		}
	}
	if err == io.EOF {
		t.finish()
	}
	return n, err
}

func (t *tap) Close() error {
	t.finish()
	return t.ReadCloser.Close()
}

func (t *tap) scan(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if len(t.line) < 1<<20 {
				t.line = append(t.line, b...)
			}
			return
		}
		t.line = append(t.line, b[:i]...)
		t.event(bytes.TrimRight(t.line, "\r"))
		t.line = t.line[:0]
		b = b[i+1:]
	}
}

func (t *tap) event(line []byte) {
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Usage *usage `json:"usage"`
		} `json:"message"`
		Usage *usage `json:"usage"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil && ev.Message.Usage != nil {
			t.u, t.seen = *ev.Message.Usage, true
		}
	case "message_delta":
		if u := ev.Usage; u != nil {
			t.seen = true
			if u.OutputTokens > 0 {
				t.u.OutputTokens = u.OutputTokens
			}
			if u.InputTokens > 0 {
				t.u.InputTokens = u.InputTokens
			}
			if u.CacheReadInputTokens > 0 {
				t.u.CacheReadInputTokens = u.CacheReadInputTokens
			}
			if u.CacheCreationInputTokens > 0 {
				t.u.CacheCreationInputTokens = u.CacheCreationInputTokens
			}
		}
	}
}

func (t *tap) finish() {
	t.once.Do(func() {
		if !t.sse {
			var body struct {
				Usage *usage `json:"usage"`
			}
			if json.Unmarshal(t.buf.Bytes(), &body) == nil && body.Usage != nil {
				t.u, t.seen = *body.Usage, true
			}
		}
		if !t.seen && t.status < 400 {
			return
		}
		t.p.bg.Add(1)
		go func() {
			defer t.p.bg.Done()
			t.record()
		}()
	})
}

func (t *tap) record() {
	rt := t.rt
	err := t.p.Ledger.Append(ledger.Usage{
		TS: time.Now(), Kind: "usage", SessionID: rt.sessionID, Scope: rt.scope, AgentID: rt.agentID,
		Routed: rt.routed, Tier: rt.tier, Model: rt.model, Effort: rt.effort, Status: t.status,
		InputTokens: t.u.InputTokens, OutputTokens: t.u.OutputTokens,
		CacheReadInputTokens: t.u.CacheReadInputTokens, CacheCreationInputTokens: t.u.CacheCreationInputTokens,
	})
	if err != nil {
		log.Printf("ledger: %v", err)
	}
	if !t.seen {
		return
	}
	cost := t.costUSD()
	if err := t.p.State.AddSpend(time.Now(), cost); err != nil {
		log.Printf("spend: %v", err)
	}
	main := rt.routed && rt.scope == catalog.ScopeMain
	if rt.sessionID == "" || !rt.routed || (!main && cost <= 0) {
		return
	}
	ctx := t.u.InputTokens + t.u.CacheReadInputTokens + t.u.CacheCreationInputTokens + t.u.OutputTokens
	if _, err := t.p.State.Update(rt.sessionID, func(s *state.Session) bool {
		s.TotalUSD += cost
		if main {
			s.ContextTokens, s.LastAPIAt = ctx, time.Now()
			s.PeakContextTokens = max(s.PeakContextTokens, ctx)
			s.SpendUSD += cost
		}
		return true
	}); err != nil {
		log.Printf("state %s: %v", short(rt.sessionID), err)
	}
}

// costUSD prices the response with the catalog (5-minute cache writes).
func (t *tap) costUSD() float64 {
	cat, err := t.p.Catalog.Get()
	if err != nil {
		return 0
	}
	m := cat.ModelByAPIID(t.rt.model)
	if m == nil || m.Price == nil {
		return 0
	}
	pr, u := m.Price, t.u
	return (float64(u.InputTokens)*pr.Input + float64(u.OutputTokens)*pr.Output +
		float64(u.CacheReadInputTokens)*pr.CacheRead + float64(u.CacheCreationInputTokens)*pr.CacheWrite5m) / 1e6
}
