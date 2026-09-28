# Sources and pitfalls

## Sources, most reliable first

1. **Anthropic**
   - Models overview (IDs, aliases, context, efforts): https://platform.claude.com/docs/en/about-claude/models/overview
   - Pricing (incl. cache writes 5m/1h, fast mode): https://platform.claude.com/docs/en/about-claude/pricing
   - Deprecations: https://platform.claude.com/docs/en/about-claude/model-deprecations
   - Effort parameter (`output_config.effort`, supported models): https://platform.claude.com/docs/en/build-with-claude/effort
   - Model announcements: https://www.anthropic.com/news
2. **Artificial Analysis** — release comparison pages give index, cost per task
   and time per task per effort level. Always record the index version
   (e.g. Intelligence Index v4.3.2) in `benchmark_version`.
   e.g. https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5
3. **Claude Code** — docs and changelog:
   - model configuration: https://code.claude.com/docs/en/model-config
   - workflows / ultracode: https://code.claude.com/docs/en/workflows
   - hooks: https://code.claude.com/docs/en/hooks
   - environment variables: https://code.claude.com/docs/en/env-vars
   - subagents: https://code.claude.com/docs/en/sub-agents
   - changelog: https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md
4. **OpenRouter / TypeSafe** — version behind `~typesafe/jev-latest`, the
   Decisions API reference, limits:
   - https://openrouter.ai/typesafe/jev-1.13
   - https://openrouter.ai/docs/api/api-reference/alphadecisions/submit-a-decisions-request
   - https://docs.typesafe.ai/models
5. **Router ledger** — `automodel report --json`. Real data for this user; it
   wins over the benchmark when they disagree (flag it explicitly).

## Claude Code facts the router depends on (re-check each refresh)

Verified 2026-09-26 on Claude Code 2.1.283 (see docs/spikes.md):

- Requests carry `X-Claude-Code-Session-Id`; subagent/workflow-agent requests
  also carry `X-Claude-Code-Agent-Id`. `metadata.user_id` is a JSON string
  with `session_id`.
- Effort is sent as `output_config.effort`, thinking as
  `{"type":"adaptive"}`, when `ANTHROPIC_CUSTOM_MODEL_OPTION_SUPPORTED_CAPABILITIES`
  declares them.
- An unknown model ID gets a 200K window unless `CLAUDE_CODE_MAX_CONTEXT_TOKENS`
  is set. That only sizes the window client-side. Server-side, first-party
  Opus 5.5 has a **native** 1M window (Claude Code's catalog:
  `context:{window:1e6, native_1m:true}`): no beta is needed, and sending the
  long-context beta (`context-1m-2025-08-07`) anyway made every routed prompt
  rewrite the conversation cache (measured 2026-09-26). The proxy only sends
  it for `long_context = "beta"` models and strips it elsewhere.
- Claude Code's model catalog is embedded in the binary. To read a model's
  entry: `strings "$(readlink -f "$(which claude)")" | grep -o
  'first_party:"claude-opus-5-5".\{0,900\}'` (context, max_output_tokens,
  capabilities such as `per_turn_effort`, `mid_conv_system`).
- Per-turn effort: an effort-only system message
  (`{"role":"system","content":[],"output_config":{"effort":…}}`) after the
  user message it applies to, beta `per-turn-control-2026-07-01` (the API docs
  call it `mid-conversation-output-config-2026-07-01`; Claude Code sends the
  former). The conversation must open with an explicit statement after the
  first user message: with the beta on, an unstated effort is rendered at the
  newest turn and the cache breaks on every prompt. Measured: effort change
  at 41k context → 51 tokens written (top-level change: 13k rewritten), and
  the per-turn level is really applied (same hard prompt: low 151 output
  tokens, per-turn xhigh 256, top-level xhigh 282).
- Any `ANTHROPIC_BASE_URL` that isn't `api.anthropic.com` makes Claude Code
  treat the provider as a gateway in **every** session: Opus loses its native
  1M window (auto-compact at 200K), tool search and other first-party features
  turn off. The installer sets `_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1`
  (honored by `Os()` in the bundle) because the proxy forwards to
  api.anthropic.com unchanged. Verify the variable still exists:
  `strings "$(readlink -f "$(which claude)")" | grep -c _CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL`.
  If it's gone, non-routed sessions lose 1M: report it as blocking.
- Claude Code caps an unknown model ("jev") at `max_tokens` 32000; the proxy
  raises routed requests to the model's `max_output`.
- Hooks and the statusline act only on sessions positively identified as the
  routed model (session state from the proxy/SessionStart/model switch, then
  the transcript's model identity, then settings). Sessions on a named model
  are never routed.
- The Agent tool's `model` only accepts `sonnet|opus|haiku|fable`, and it has
  no effort field (an `effort` key in `updatedInput` is ignored).
- Workflow `agent()` options accept `model` and `effort`.
- Hook inputs don't carry the model, except SessionStart (sometimes) and
  Pre/PostModelSwitch (`to_model`).
- The compaction summary is the `type:"user"`, `isCompactSummary:true` entry
  after `system/compact_boundary` in the transcript.
- `--resume` keeps the session ID; SessionStart(resume) reports
  `prompt_cache_likely_expired`.
- SessionStart(compact) runs before the compaction summary is in the
  transcript: the hook decides from a detached copy that waits for it.
- Claude Code 2.1.284: the `sonnet` alias is Sonnet 5.5 (first-party), and
  the Explore subagent now inherits an unknown session model (the routed
  "jev") instead of switching to Opus; the agent hook's alias still wins.
- Sonnet 5.5 rejects `thinking: disabled` (400), can't read other models'
  thinking blocks (dropped silently), and returns 400 when anything before a
  replayed Sonnet 5.5 thinking block changed (prefix binding): the proxy's
  effort-only messages must stay byte-identical once placed.

If any of these change, the proxy or hooks need updating: say so in the report.

## Pitfalls

- **Stale aggregator prices.** Aggregators keep "introductory" prices after
  they change or become permanent (Sonnet 5's introductory price, which became
  permanent, is an example). Always cross-check with the official pricing page.
- **Equal effort is not equal cost.** Compare (model, effort) pairs on the
  frontier, never "Sonnet high vs Opus high".
- **Price per token across generations.** Tokenizers differ between model
  generations; compare cost per task, not price per million tokens.
- **Mixing benchmark versions.** Never compare measurements from different
  index versions; frontier.py ignores other versions and says so.
- **Cache read parity.** When cache reads cost the same on two models (Sonnet 5
  and Opus 5.5: $0.20/M), the cheaper model's real advantage in long Claude
  Code sessions — dominated by cache reads — shrinks further.
- **The index is not Claude Code.** It mixes knowledge work and terminal
  agentic tasks. Let the ledger confirm or correct.
