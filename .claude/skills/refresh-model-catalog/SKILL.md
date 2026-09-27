---
name: refresh-model-catalog
description: Refresh automodel's catalog.toml (models, prices, benchmark measurements, routing tiers, modes, Jev criteria and thresholds). Use when a model is released, deprecated or repriced ("Sonnet 5.5 is out", "refresh the catalog", "review the model choices"), when a new Jev version ships, when `automodel catalog check` or frontier.py recommends a refresh, or as a scheduled monthly review.
---

# Refresh the model catalog

`catalog.toml` is the router's single source of truth. This skill re-does the
model research, proposes a new catalog with evidence, and applies it only with
the user's explicit approval.

## Absolute rule

**Never write `catalog.toml` without the user's explicit approval.** Work on
`catalog.proposed.toml`. In a scheduled/unattended run, stop at the report.

## Procedure

1. **Current state**
   - Read `catalog.toml` and the last entry of `catalog-history.md`.
   - `python3 .claude/skills/refresh-model-catalog/scripts/frontier.py catalog.toml`
     (add `--json` to parse).
   - `automodel report --json` (real usage: tier mix, fallback rate, cache hit
     rate, tokens per tier, shadow agreement).

2. **Inventory since `meta.last_refresh`** — every fact with its primary
   source (see `references/sources.md`):
   - new models: API ID, alias, context window, efforts, fast mode;
   - context windows: `context` is the **largest** window the model accepts,
     never the standard one, and `long_context` says how: `"native"` (no beta;
     sending the beta anyway defeats the prompt cache) or `"beta"` (needs
     `meta.long_context_beta`). Claude Code's own model catalog is the source
     for first-party behaviour (`native_1m`, `supports_1m_beta`, see
     references/sources.md);
   - per-turn effort: set `per_turn_effort = true` only for models that accept
     effort-only system messages mid-conversation (Claude Code's catalog
     lists `per_turn_effort` in the model's capabilities), and keep
     `meta.per_turn_effort_beta` equal to the beta Claude Code sends
     (`per-turn-control-…`). Verify on a real session (step 8);
   - `max_output`: the model's output ceiling (routed requests are raised to
     it; Claude Code caps an unknown model at 32k);
   - deprecations and retirements;
   - price changes (input, output, cache read, cache writes 5m/1h);
   - Claude Code changes: effort levels, ultracode and workflow semantics,
     custom-model variables, hooks (inputs/outputs), known bugs;
   - new Jev version or Decisions API change.

3. **Measurements**
   - The primary benchmark (`meta.benchmark`/`benchmark_version`) sets tier
     costs. Also record agentic-coding benchmarks as cross-checks, each under
     its own `benchmark_version` (never mixed with the primary): e.g.
     CursorBench (cost per task at list prices), Artificial Analysis' Coding
     Agent Index (runs in Claude Code: the closest to real use), Terminal-Bench,
     SWE-bench Verified/Pro, and the system-card numbers.
     `frontier.py catalog.proposed.toml --version <name>` shows each.
   - Record tokens and steps per task where published: a model cheaper per
     token can cost **more per task** (more tokens, more turns). Say so.
   - Same benchmark version: add/update `[[measurements]]`.
   - New benchmark version: re-collect **all** active and candidate configs,
     then update `meta.benchmark_version`. Versions never compare.
   - A model without measurements may stay active for a niche, with an
     explicit `reason`, to be validated by the ledger.

4. **Frontier** — on `catalog.proposed.toml`, for the primary version and
   each cross-check version:
   - run frontier.py; mark dominated models `status = "dominated"` with a
     reason that quotes numbers;
   - judge the quasi-dominated cases it lists (the tool doesn't decide them);
   - make `criteria` restrictive on steps with poor marginal returns;
   - cross-check with ledger data; when real usage contradicts the
     benchmark, say so explicitly — the ledger wins for this user;
   - price this user's real traffic at the candidate's prices:
     `automodel report --json` (`tokens_by_tier`) × the candidate's input,
     output, cache read and cache write prices, then apply the candidate's
     tokens/steps multiplier from the benchmarks. A lower list price that a
     1.2–1.6× step multiplier cancels is not a saving.

   **Admission rule for a new model or tier** (the owner's rule): a
   (model, effort) config earns a tier only if, with numbers, it sits on the
   cost/quality frontier against the current tiers **for the work that tier
   would get** (agentic coding for main and subagent tiers), on the primary
   benchmark *and* not contradicted by the agentic-coding cross-checks. If
   it is worse at every effort, or costs more for the same work, it stays
   `dominated` with a reason quoting the numbers. "Opus dominates everywhere"
   is a valid outcome: record the figures and change no tier. For the main
   scope also check structure: per-turn effort support (otherwise every
   effort change rewrites the cache), cache-read price, and what a model
   switch costs at a typical context (the whole context written again).

5. **Tiers**
   - Keep the main session on one model (only effort varies): switching model
     loses the cache and the thinking blocks. Add a second main model only if
     the frontier clearly requires it.
   - Subagent tiers need a model with an `alias` (the Agent tool only accepts
     `sonnet|opus|haiku|fable`).
   - Modes (`[modes.*]`, ultracode) are layered on any tier and any model:
     never model them as a tier. Adapt `effort`, `min_tier` and the yes/no
     criteria if Claude Code's ultracode semantics changed.
   - `criteria` are the levels of Jev's Score question, lowest first:
     describe **situations** ("a race condition", "a commit message"), not
     degrees ("moderately complex"); keep neighbouring levels mutually
     exclusive; English.
   - Tiers without a measurement need a `cost` (relative cost per task, same
     unit as `cost_per_task`), otherwise it is interpolated (warning).
   - `python3 …/frontier.py catalog.proposed.toml` must exit 0, and
     `automodel catalog check --catalog catalog.proposed.toml` must report no
     error.

6. **Decision quality** — whenever criteria, modes, questions or the Jev
   version change, and at every periodic refresh:
   - `automodel eval --catalog catalog.proposed.toml` against
     `automodel eval` (current catalog); `--format choice` compares with the
     v1 question. No regression is acceptable on "router decision acceptable"; mean
     confidence should not drop.
   - Thresholds come from the eval, never from intuition: a mode's
     `threshold` and `meta.continues_threshold` sit in the gap between the
     yes-cases' and no-cases' probabilities the eval prints (e.g. yes ≥ 0.85,
     no ≤ 0.68 → 0.75). If the gap closes, rewrite the yes/no criteria first.
   - Add cases to `testdata/eval/routing.jsonl` for every misroute found in
     the ledger (one line: state, want, accept, modes, continues).
   - Calibrate `meta.underprovision_penalty` from the ledger: warm decisions
     that switch *up* right after a switch *down* (the lower tier was not
     enough) argue for a higher penalty; routine prompts kept high argue for
     a lower one. Report the evidence, change it only with approval.

7. **Report to the user** — also written to
   `docs/research/YYYY-MM-<topic>.md` (sources with their dates, frontier
   tables per version, per-scope verdicts, the diff, open questions):
   - summary of detected changes, with sources;
   - frontier table and marginal returns;
   - `diff -u catalog.toml catalog.proposed.toml`;
   - router impact (tiers, hooks, thresholds);
   - open questions and how to settle them.

   On approval: replace `catalog.toml`, set `meta.last_refresh`, validate
   again, append an entry to `catalog-history.md` (template below). The proxy
   and hooks hot-reload the catalog; nothing to restart.

8. **Real-session check** after any change to `per_turn_effort`,
   `long_context` or the betas: run a routed session with a few prompts and
   read the usage lines of the ledger. A request that rewrites ~the whole
   conversation means the cache broke: revert and report.
   - **Isolate it.** Build a dev binary (`go build`, version `dev`: it never
     self-updates nor touches settings) and run it with its own config
     (`listen` on another port, `custom_model_id = "jevtest"`, its own
     `state_dir`, `[update] auto = false`). Start Claude Code with
     `claude --settings <file> --model jevtest`, the file setting
     `ANTHROPIC_BASE_URL`, `ANTHROPIC_CUSTOM_MODEL_OPTION=jevtest` and the
     hooks/statusline pointing at the dev binary and config. Never run a
     released binary or `install` against the real `~/.claude/settings.json`.
     Check the real settings' sha256 before and after.
   - **Read it right.** Behind any custom `ANTHROPIC_BASE_URL` Claude Code
     turns off its server-side message threads (it enables them only for
     api.anthropic.com), so the first prompts of a fresh session rewrite
     ~13k tokens even when nothing is wrong. Compare with the same prompts
     routed as passthrough (`--model opus` through the same proxy) and judge
     on prompts sent after the first minute, or on the ledger's cache hit
     rate over real sessions (`automodel report`, ~98% is normal).
     `AUTOMODEL_DUMP_DIR=<dir>` on the proxy writes every upstream request
     (body and headers, credentials redacted) to diff two requests.

9. **New Jev version** — never edit `meta.jev_model` directly:
   1. set `jev_shadow_model = "<new version>"` in the router config
      (`~/.config/automodel/config.toml`); both versions decide, only the
      current one is applied, both are logged;
   2. after enough decisions, compare with `automodel report --json`
      (`shadow.agree_rate`, `shadow.disagreements`, confidence buckets);
   3. run `automodel eval` on both versions; recalibrate mode thresholds,
      `continues_threshold`, `features.warm_min_confidence` and (v1 policy)
      `theta_act` / `theta_low` for the new version;
   4. switch `meta.jev_model`, clear `jev_shadow_model`, log it in the history.

## History entry template

```markdown
## YYYY-MM-DD — <one-line summary>
- Trigger: <new model / price / retirement / periodic refresh / new Jev version>
- Changes: <models, tiers, criteria>
- Reasons: <frontier numbers, ledger data>
- Sources: <URLs>
- Still to verify: <...>
```

## Pitfalls

**A cache-breaking change is the most expensive mistake.** The router's
value depends on keeping the prompt cache: a beta header the model doesn't
need (the long-context beta on a native-1M model did exactly this), a
top-level effort change on a per-turn model, or a model switch each rewrite
the whole conversation at cache-write price. Step 8 is not optional.

**Context windows are a hard constraint, not a cost trade-off.** Main tiers
must sit on models with at least `meta.main_min_context` (1M): routing a long
session onto a smaller window forces compaction loops and breaks it. Never
lower `context`, `main_min_context` or drop `long_context_beta` to make a
cheaper model eligible for the main scope; validation rejects main tiers
below the minimum. A 200K model (Haiku) belongs in the subagent scope only.

Read `references/sources.md` before collecting data. In short: aggregators
carry stale prices; compare (model, effort) pairs, not models at equal effort;
compare cost per task, not price per token, across tokenizer generations.
