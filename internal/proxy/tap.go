package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
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
	t := &tap{ReadCloser: resp.Body, p: p, rt: rt, status: resp.StatusCode}
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		t.sse = true
	case strings.HasPrefix(ct, "application/json"):
	default:
		return nil
	}
	resp.Body = t
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
		go t.record()
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
	if rt.routed && rt.scope == catalog.ScopeMain && rt.sessionID != "" && t.seen {
		ctx := t.u.InputTokens + t.u.CacheReadInputTokens + t.u.CacheCreationInputTokens + t.u.OutputTokens
		if _, err := t.p.State.Update(rt.sessionID, func(s *state.Session) bool {
			s.ContextTokens, s.LastAPIAt = ctx, time.Now()
			s.PeakContextTokens = max(s.PeakContextTokens, ctx)
			s.SpendUSD += t.costUSD()
			return true
		}); err != nil {
			log.Printf("state %s: %v", short(rt.sessionID), err)
		}
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
