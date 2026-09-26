# Catalog history

## 2026-09-26 — Initial catalog: Opus 5.5 across all main tiers
- Trigger: initial setup.
- Changes: Opus 5.5 active (main low→max, ultracode = xhigh + workflows; subagent opus-low→opus-max). Haiku 4.5 active for mechanical subagent work only. Sonnet 5, Opus 5 and Opus 4.8 dominated; Fable 5.1 excluded. Defaults: main `high`, subagent `opus-medium`.
- Reasons: on the Artificial Analysis Intelligence Index v4.3.2, every Opus 5.5 effort level is on the frontier (low 42/$0.55, medium 51/$1.34, high 54/$1.82, xhigh 56/$3.46, max 58/$5.98). Sonnet 5 medium→max (28→38, $1.00→$5.09) is dominated. Sonnet 5 low (24/$0.51) is quasi-dominated (7% cheaper than Opus 5.5 low for −18 points), so it is excluded. Sonnet 5's cache read costs the same as Opus 5.5's ($0.20/M). Marginal returns: low→medium +9 ×2.44, medium→high +3 ×1.36, high→xhigh +2 ×1.9, xhigh→max +2 ×1.73.
- Sources: https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5, https://platform.claude.com/docs/en/about-claude/pricing
- Still to verify: Haiku 4.5's position (not measured on v4.3.2), how fast mode counts on a Max subscription (`fast.allowed = false` until then), Opus 5 / Opus 4.8 figures, the frontier once Sonnet 5.5 and Haiku 5.5 are out.

## 2026-09-26 — v2: ultracode becomes a mode, Score criteria, per-turn effort, native 1M
- Trigger: a routing review (long sessions lost 1M; ultracode was listed as a model; warm re-decisions).
- Changes: `[tiers.main.ultracode]` removed and replaced by `[modes.ultracode]` (any main tier from `high` up, effort raised to `xhigh`, Jev yes/no threshold 0.75). All tier `criteria` rewritten as situations (they are now the levels of Jev's Score question). Opus 5.5 `long_context = "native"`, `per_turn_effort = true`, `max_output = 128000`; Sonnet 5 `long_context = "native"`; Haiku 4.5 `max_output = 64000`, subagent tier `cost = 0.15` (no measurement). Meta: `per_turn_effort_beta`, `underprovision_penalty = 3.0`, `continues_threshold = 0.7`.
- Reasons: `automodel eval` on 56 labeled cases, with every router decision acceptable (v1 Choice with ultracode as an option: 95%) and mean confidence 0.92 (v1 0.86). Ultracode yes-cases ≥ 0.85 and no-cases ≤ 0.68, so the threshold is 0.75. Continuation yes ≥ 0.83 and no ≤ 0.51, so 0.7. The long-context beta on native-1M Opus 5.5 broke the conversation cache (docs/spikes.md S9). Per-turn effort keeps it (S10–S11).
- Sources: Claude Code 2.1.283 embedded model catalog; https://platform.claude.com/docs/en/build-with-claude/effort (per-message effort); docs.typesafe.ai (Score, Noul, confidence).
- Still to verify: `underprovision_penalty` against ledger data (up-switches right after down-switches); the eval set is small and self-labeled, so grow it from real misroutes.
