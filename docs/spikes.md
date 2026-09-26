# Spikes (spec §10): results

Measured on 2026-09-26 with Claude Code 2.1.283 behind a logging proxy
(`claude -p --settings … --model jev`), plus the docs. Traces are kept out of
the repo.

| # | Question | Result | Consequence |
|---|---|---|---|
| S1 | `session_id` in API requests? | **Yes.** `X-Claude-Code-Session-Id` header on every request. `metadata.user_id` is a JSON string `{"device_id","account_uuid","session_id"}`. Subagent requests also carry `X-Claude-Code-Agent-Id`. | The proxy reads the header, then metadata, then the prompt hash (fallback). The agent ID lets the proxy tell main from subagent and bind a per-subagent effort. |
| S2 | Context window for a custom ID `jev`? | **200K**, with a warning: `"jev" isn't described by this version's model catalog… set CLAUDE_CODE_MAX_CONTEXT_TOKENS…` | `CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000` in the settings (`automodel install` adds it). Auto-compaction then follows this window. |
| S2b | Does the API then serve 1M to `jev`? | **No.** Claude Code sends the `context-1m-2025-08-07` beta only for names ending in `[1m]`; without it the API caps the routed Opus at 200K while Claude Code believes 1M: "prompt too long", reactive compactions. Separately, a non-Anthropic `ANTHROPIC_BASE_URL` turns off Opus's native 1M (and other first-party features) in **every** session. | The proxy adds `meta.long_context_beta` for routed models above 200K and strips it below. The installer sets `_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1` (upstream is api.anthropic.com). Main tiers must be ≥ `meta.main_min_context` (1M). |
| S3 | Shape of the effort field | `output_config: {"effort": "<level>"}` + `thinking: {"type":"adaptive","display":"omitted"}` + `context_management.edits[clear_thinking_20251015]`. Beta header `effort-2025-11-24` already present. With no `--effort`, Claude Code sends `xhigh` for an unknown ID. | The proxy rewrites `output_config.effort`. For a model without effort (Haiku 4.5) it removes `effort`, adaptive `thinking` and the `clear_thinking` edits. |
| S4 | `updatedInput.model` on `Agent`? Per-invocation effort? | **Model: yes, aliases only** (`sonnet\|opus\|haiku\|fable`). A full ID fails validation. **Effort: no**: `updatedInput.effort` is ignored, and the opus subagent received `high`. | The hook sets the alias (`alias` field in the catalog, required for subagent tiers) and registers the effort. On the subagent's first request, the proxy matches the prompt, binds its agent ID and rewrites the effort. No need for "one agent per tier". |
| S5 | PreCompact payload, summary location | PreCompact: `{"trigger":"manual\|auto","custom_instructions":…}`. Transcript: `type:"system", subtype:"compact_boundary"`, then `type:"user", isCompactSummary:true` (content = summary). SessionStart `source:"compact"` follows and includes `"model":"jev"`. | Marker set by PreCompact (and by SessionStart(compact) as a backup). The summary is read from the transcript tail. |
| S6 | Same `session_id` after `--resume`? | **Yes.** SessionStart(resume) also provides `prompt_cache_likely_expired`, `seconds_since_last_response` and `context_tokens`. (`--fork-session` creates a new ID.) | `prompt_cache_likely_expired=true` → `cold` trigger at the next prompt, in addition to the `cache_ttl` measurement. |
| S7 | Does the injected ultracode opt-in trigger workflows reliably? | **Not measured** (costly: real workflows). | `scripts/spike-s7.sh` compares 10 prompts in routed mode (forced to ultracode by `min_tier`) and with `--effort ultracode`. The injected text reproduces the native setting's reminder ("Ultracode is on for this session… standing opt-in"). |
| S8 | How fast mode counts on Max | **Not settled.** | `fast.allowed = false` in the catalog; the router never enables fast mode. |

## Other observations that shaped the implementation

- **Hooks don't receive the model** (except SessionStart sometimes, and
  Pre/PostModelSwitch). The env var `CLAUDE_EFFORT` exists, but there is no
  `CLAUDE_MODEL`. The router combines, in order: model observed by the proxy /
  statusline / SessionStart / PostModelSwitch → `model` attachment in the
  transcript (`identity.modelId`) → `--model` flag of the parent process
  (`$CLAUDE_PID`) → `ANTHROPIC_MODEL` → settings files. If still unknown, it
  decides anyway but holds the ultracode notice until confirmation.
- **UserPromptSubmit also fires for subagent hand-backs**
  (`<agent-message from=…>`). These prompts never trigger a redecision.
- **Subagents with no explicit model send `model: "jev"`** (inherited). The
  proxy recognises them through `X-Claude-Code-Agent-Id` and applies the bound
  decision, otherwise the main decision (native inheritance semantics, e.g.
  workflow agents under ultracode → xhigh).
- **Claude Code sends `Accept-Encoding`**. The proxy removes it on
  `/v1/messages` so Go's transport negotiates and decompresses the stream
  itself; otherwise the usage tap can't read the SSE.
- The prompt cache survives the proxy: a second session at the same tier read
  88K cached tokens (`cache_read_input_tokens`) on its first request.

## 2026-09-26 — v2: cache behaviour of effort changes (measured through the proxy)

| # | Question | Result | Consequence |
|---|---|---|---|
| S9 | Does the long-context beta on first-party Opus 5.5 cost anything? | **Yes: every prompt rewrote the conversation cache** (28k read, 13k written per prompt; plain `opus` through the same proxy: 41k read, ~150 written). Opus 5.5 has a native 1M window. | Catalog `long_context = "native"`: the proxy sends the beta only to `"beta"` models and strips it elsewhere. |
| S10 | Can effort change mid-conversation without losing the cache? | **Yes on Opus 5.5** with effort-only system messages (`per-turn-control-2026-07-01`, placed after the user message; the conversation must open with an explicit statement, otherwise the unstated effort is rendered at the newest turn and the cache breaks every prompt). Switch at 41k context: 51 tokens written. | Proxy per-turn effort (`features.per_turn_effort`); warm decisions become affordable. |
| S11 | Is the per-turn level really applied? | Same hard prompt: low 151 output tokens, per-turn xhigh 256, top-level xhigh 282 (with 13k cache rewrite). | Per-turn effort ≈ top-level effort, without the rebuild. |
| S12 | Claude Code's `max_tokens` for an unknown model | 32000 (Opus 5.5 gets 128000). | Proxy raises routed requests to the catalog `max_output`. |
