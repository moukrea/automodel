---
name: refresh-model-catalog
description: Refresh automodel's catalog.toml (models, prices, benchmark measurements, routing tiers, modes, Jev criteria and thresholds). Use when a model is released, deprecated or repriced ("Sonnet 5.5 is out", "refresh the catalog", "review the model choices"), when a new Jev version ships, when `automodel catalog check` or frontier.py recommends a refresh, or as a scheduled monthly review.
---

# Refresh the model catalog

`catalog.toml` is the router's single source of truth. This skill re-does the
model research, proposes a new catalog with evidence, and applies it only with
the user's explicit approval.

## Absolute rule

**Never write the catalog without the user's explicit approval.** Work on a
proposed copy. In a scheduled/unattended run, stop at the report.

## Whose catalog: maintainer or user

- **In the automodel repository** (you're working on automodel itself):
  `catalog.toml` is the **default tuning**, shipped in every release. Propose
  in `catalog.proposed.toml`; on approval it replaces `catalog.toml`.
- **Anywhere else** (a user of automodel tuning their own routing): the result
  is the user's **custom tuning**, never the default. `automodel tuning path`
  gives its file (`automodel tuning init` creates one: a template, or
  `--full` for a whole copy). Keep it **partial when you can**: only the keys
  that change, layered over the default, so the user still gets automodel's
  future improvements for everything else. Propose in `<path>.proposed`
  (`automodel tuning diff` and `automodel eval --catalog <path>.proposed`
  work on a partial file too). On approval write it, then
  `automodel tuning use custom`; `automodel tuning use default` goes back.
  Tell the user which tuning is in use at the end (`automodel tuning`).

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
   - Look for **per-effort** data for every candidate *and* the models it
     competes with: a single max-vs-max number says nothing about the lower
     tiers. Where it hides: the chart data embedded in the vendor's
     announcement page (score and cost per effort, often several evals), the
     per-eval fields of AA's model-page JSON (e.g. `terminalbench-4-0` with
     its own cost and time per task), and the Coding Agent Index rows (one
     per effort). See `references/sources.md`.
   - **A model released in the last week is provisional.** AA publishes times
     per task and Coding Agent Index runs a day or more after a release, and
     sometimes re-runs a pre-release build. "Not published" is not evidence:
     re-read the sources 1–3 days after the release, and revisit the decision
     with what came out.
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
   - Put the candidate in a table: one row per effort, one column per
     benchmark with per-effort data for both models, ✓ or ✗ in each cell
     with the dominating config. Decide from the whole table, never from one
     column.
   - **Being under the line between two frontier neighbours (the convex
     hull) is not a reason to leave a frontier config out.** That argument
     assumes the budget is spread over a random mix of tiers; a router picks
     one config per task, so a frontier point helps on the tasks it fits.
     Whether Jev can tell those tasks apart is the eval's question (step 6).
     This mistake kept Sonnet 5.5 high out on 2026-09-28 on the index alone,
     while CursorBench, Terminal-Bench and FrontierCode put it above the line.
   - Tier costs must rise with rank: the policy reads rank as capability, so
     a tier that costs less than a lower-ranked one turns an underprovision
     into an "overprovision" (validation warns). A config dominated on the
     primary benchmark can't take a slot between the two tiers that
     dominate it.

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
     exclusive; English. Jev judges each level **on its own**: it never sees
     the level numbers or the neighbours, so "harder than the previous level"
     or "levels are ordered" means nothing to it. How to write and test them:
     `references/routing-eval.md`. A wording change is kept only if it wins
     on the train split *and* holds on the held-out split.
   - Tiers without a measurement need a `cost` (relative cost per task, same
     unit as `cost_per_task`), otherwise it is interpolated (warning).
   - An **asked tier** (`question` + `criteria` as its yes side + `no` +
     `threshold`) is not a Score level: Jev answers its own yes/no in the
     same call, and it replaces the scored tier ranked just above it when the
     yes-probability reaches `threshold`. Use it for a tier on another model
     that only fits clear-cut prompts (main-session Haiku): adding it as a
     level shifts Jev's whole scale. Its threshold sits in the train gap like
     a mode's. The router only moves a warm session onto an asked tier of
     another model when it is already there (a turn on Haiku with a warm
     cache costs about Opus low, and coming back rebuilds everything).
   - `python3 …/frontier.py catalog.proposed.toml` must exit 0, and
     `automodel catalog check --catalog catalog.proposed.toml` must report no
     error.

6. **Decision quality** — whenever criteria, modes, questions, the policy
   (`underprovision_penalty`, gates) or the Jev version change, and at every
   periodic refresh. Details and pitfalls: `references/routing-eval.md`.
   - Build a dev binary and run the eval with an isolated config (never the
     real `~/.config/automodel`: the eval saves the evaluated catalog as the
     last-good copy of its state dir):
     `automodel eval --catalog catalog.proposed.toml --repeat 3 --summary`,
     once with `--split train` and once with `--split test`, and the same
     for the current `catalog.toml`. Jev varies a little between calls:
     compare runs of 3 repeats, never single runs.
   - **Read the metrics, not just "acceptable".** For each scope: exact
     accuracy and rank error of Jev's top level *and* of the router's
     decision (the gap between the two is the policy's doing); recall per
     tier; the confusion matrices (a tier skipped, e.g. low → high with
     medium empty, is the collapse); **decision share vs label share** per
     tier; ECE of the top probability.
   - **Regression gate** (mandatory):
     `automodel eval --catalog catalog.proposed.toml --split test --repeat 3 --summary --check`
     must print "regression gate: pass" (decision exact ≥ 88%, recall ≥ 80%
     per tier, each tier's decision share within 6 points of its label
     share, rank error ≤ 0.12, no follow-up decided below its label or
     below the work it holds, and no effort, mode or model request
     confirmed on a prompt that doesn't make one; run the train split with
     `--check` too for the last two). A proposal that fails is not proposed. If
     a new Jev version or tier set makes a gate unreachable, report it with
     the tables; changing `eval.DefaultGate` needs the user's approval.
   - Tune on `--split train` only. The `test` cases are held out: look at
     their aggregate numbers, never at their per-case rows while tuning, or
     the gate stops meaning anything. New cases go to train unless you add a
     batch big enough to split (alternate train/test within each label).
   - Thresholds come from the eval, never from intuition: a mode's
     `threshold`, an asked tier's `threshold`, `meta.explicit_threshold`
     (effort and mode requests) and `meta.explicit_model_threshold` (model
     requests, stricter: a mention must never become the model in use) sit
     in the gap between the yes-cases' and no-cases' probabilities on the
     train split (print them with `--json`); if there is no gap, pick the
     value with the fewest false no's and rewrite the yes/no criteria.
     Compare thresholds and rules on the same answers with `automodel eval
     --catalog <variant> --answers <saved --json run>` (no Jev call, no
     noise); wording changes need live runs of 3 repeats. `meta.relation_separate_threshold` (default 0.6) is
     where P(new_task) + P(wrap_up) separates the separate cases from the
     follow-ups (no follow-up below the label: the gate checks it).
     `features.warm_min_confidence`
     (costly switches only) sits where exact accuracy by confidence bucket
     jumps (0.8 for jev-1.13: 39–46% below, 83%+ above).
   - `meta.underprovision_penalty`: compare the cost-aware rule at several
     penalties against Jev's argmax on both splits (exact accuracy and the
     realized cost loss against the labels, `references/routing-eval.md`).
     A penalty that moves decisions above the labels (decision share of
     high/xhigh above label share, "above the label" count growing) is too
     high; 1.5 was the measured optimum for jev-1.13. Cross-check with the
     ledger: warm switches up right after a switch down argue for more.
   - Add cases to `testdata/eval/routing.jsonl` for every misroute found in
     the ledger (state, want, accept, note, modes, relation, explicit).
     Invent the text: never copy real prompts (the repository is public). Before
     trusting ledger shares, drop demo and test sessions (sessions whose
     transcript lives in a demo project): in 2026-09, 77 of 98 decisions
     came from scripted demos built on extreme prompts.
   - Review `~/.local/state/automodel/flagged.jsonl` (decisions the user
     flagged with `automodel flag`; `automodel eval --cases` runs them);
     turn flagged cases into anonymised eval cases (never copy the user's
     text into the public repo).

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
   3. run `automodel eval` on both versions (`--repeat 3`, train and test,
      `--check`); recalibrate mode thresholds,
      `relation_separate_threshold`, `explicit_threshold`,
      `explicit_model_threshold`,
      `features.warm_min_confidence`, `underprovision_penalty` and (v1
      policy) `theta_act` / `theta_low` for the new version;
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

**"Acceptable" hides a collapsing router.** A router can score 90%
acceptable while it never picks medium: accept sets absorb the adjacent
error on every case. Check exact accuracy, recall per tier and decision
share against label share at every refresh (`--check` does). And look at
the gap between Jev's top level and the router's decision: in 2026-09 Jev was
right and the policy (penalty 3.0, gates on free switches) was not.

**Context windows are a hard constraint, not a cost trade-off.** Main tiers
must sit on models with at least `meta.main_min_context` (1M): routing a long
session onto a smaller window forces compaction loops and breaks it. Never
lower `context`, `main_min_context` or drop `long_context_beta` to make a
cheaper model eligible for the main scope. The one exception is a tier with
`max_context` below its model's window (validation requires it, and the
default tier can't have one): routing leaves it once the context passes
`max_context`, and the proxy sends a request that outgrows it to the next
tier that fits, within the turn. Keep a margin (150K on Haiku's 200K).

Read `references/sources.md` before collecting data. In short: aggregators
carry stale prices; compare (model, effort) pairs, not models at equal effort;
compare cost per task, not price per token, across tokenizer generations.
