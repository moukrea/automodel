package hooks

import (
	"context"

	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
)

// Lifecycle hooks never output anything; they only keep session state for
// the routing hooks, and only sessions on the routed model get routing flags.

// PreCompact marks the session so the next prompt re-decides on the
// compaction summary (only the system + tools prefix survives compaction).
func PreCompact(_ context.Context, env *router.Env, in *Input) (*Output, error) {
	sess, err := env.State.Load(in.SessionID)
	if err != nil {
		return nil, err
	}
	if !env.IsCustom(detectModel(env, sess, in, transcriptIfUnknown(sess, in)).Model) {
		return nil, nil
	}
	_, err = env.State.Update(in.SessionID, func(s *state.Session) bool {
		s.CompactPending, s.CompactTrigger = true, in.Trigger
		s.Compactions++
		if s.ContextTokens > s.PeakContextTokens {
			s.PeakContextTokens = s.ContextTokens
		}
		return true
	})
	return nil, err
}

// SessionStart records the model when Claude Code provides it, marks
// compactions (belt and braces with PreCompact) and flags resumed sessions
// whose cache Claude Code reports as expired.
func SessionStart(ctx context.Context, env *router.Env, in *Input) (*Output, error) {
	_, err := env.State.Update(in.SessionID, func(s *state.Session) bool {
		changed := false
		if in.Model != "" && s.Model != in.Model {
			s.Model, s.ModelSource, changed = in.Model, "session_start", true
		}
		// The model is recorded for every session (detection needs it);
		// the routing flags only for sessions on the routed model.
		if !env.IsCustom(s.Model) {
			return changed
		}
		switch in.Source {
		case "compact":
			if !s.CompactPending {
				s.CompactPending, s.CompactTrigger, changed = true, "auto", true
				s.Compactions++
			}
		case "resume":
			if in.CacheExpired != nil && *in.CacheExpired && s.Main != nil {
				s.ColdHint, changed = true, true
			}
		}
		return changed
	})
	if err == nil && in.Source == "compact" {
		compactDecision(ctx, env, in)
	}
	return nil, err
}

// compactDecision re-decides right after a compaction, from its summary:
// the status line shows the pick for the work ahead at once. The next
// prompt still re-decides for free (the cache is rebuilt anyway), with the
// prompt itself.
func compactDecision(ctx context.Context, env *router.Env, in *Input) {
	sess, err := env.State.Load(in.SessionID)
	if err != nil || sess.Main == nil || sess.Pin != "" || !env.IsCustom(sess.Model) || !env.Cfg.Features.WarmDecisions {
		return
	}
	tr := readTranscript(in.TranscriptPath)
	if tr == nil || tr.CompactSummary == "" {
		return
	}
	pin := *in
	pin.Prompt = ""
	req := mainRequest(env, &pin, sess, tr, sess.Repo, "compact")
	dec, out := env.Decide(ctx, req)
	if !out.Changed || dec == nil {
		return
	}
	env.State.Update(in.SessionID, func(s *state.Session) bool {
		dec.Epoch = 1
		if s.Main != nil {
			dec.Epoch = s.Main.Epoch + 1
		}
		s.Main = dec
		s.ResetEffortEpoch()
		s.ContextTokens = 0 // stale until the next response
		return true
	})
}

// ModelSwitch (PostModelSwitch) tracks /model changes. Switching onto the
// routed model with a cold cache re-decides at the next prompt.
func ModelSwitch(_ context.Context, env *router.Env, in *Input) (*Output, error) {
	if in.ToModel == "" {
		return nil, nil
	}
	_, err := env.State.Update(in.SessionID, func(s *state.Session) bool {
		s.Model, s.ModelSource = in.ToModel, "model_switch"
		if env.IsCustom(in.ToModel) && s.Main != nil && (in.CacheWarm == nil || !*in.CacheWarm) {
			s.ColdHint = true
		}
		return true
	})
	return nil, err
}
