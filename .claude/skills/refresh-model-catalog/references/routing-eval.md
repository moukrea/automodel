# Evaluating routing decisions

How to measure whether Jev and the router pick the right tier, and how to
change criteria, questions and policy without fooling yourself. Background
and full numbers: `docs/research/2026-09-routing-quality.md`.

## The benchmark

`testdata/eval/routing.jsonl`, one case per line:

```json
{"id": "l-med-tsv", "scope": "main", "warm": true,
 "state": {"phase": "warm", "task": "...", "recent_prompts": ["..."], "last_assistant": "...",
           "current": {"tier": "high", "effort": "high"}, "session": {...}, "repo": {...}},
 "want": "medium", "accept": ["low", "medium"], "continues": true,
 "note": "Small extension of finished work.", "split": "test", "modes": {"ultracode": false}}
```

- `state` has the shape the hooks send (`internal/hooks/decide.go`
  `mainRequest`, `agent.go`, `workflow.go`): keep it realistic. Real states
  carry long `last_assistant` messages and compaction summaries full of
  technical words; a benchmark of short clean prompts scores 90%+ and hides
  the failures that matter.
- `want` is the tier a careful engineer would pick; `accept` lists the
  tiers that are honestly defensible too; `note` says why. Label rubric
  (main scope, Opus efforts):
  - **low**: no reasoning beyond recall, or a fully specified mechanical
    edit or command (explanations, commit messages, git, renames, running a
    command and reporting);
  - **medium**: routine coding with a clear path in one area (a flag, a
    field, tests for existing code, a fix whose cause is visible, repeating
    a pattern);
  - **high**: several files or components, design choices and edge cases,
    or a reproducible bug of unknown cause (refactors, integrations,
    migrations, performance, implementing an agreed design);
  - **xhigh**: a subtle mistake is expensive (concurrency, security, money
    or data integrity, intermittent bugs of unknown cause, architecture);
  - **max**: defeated repeated expert attempts, research-grade, proofs.
  A follow-up does not inherit the difficulty of the earlier work (a commit
  message after a race fix is low); a go-ahead takes on the work it approves.
- `split`: `train` (tune on it) or `test` (held out). Keep labels balanced
  within each split.
- Never copy real prompts or transcripts: invent the text.

## Running it

Use a dev build and an isolated config: `automodel eval` loads the catalog
through the store, which saves it as the last-good copy in the config's
state dir.

```sh
go build -o /tmp/am ./cmd/automodel
mkdir -p /tmp/amcfg && printf 'state_dir = "./state"\n' > /tmp/amcfg/config.toml
export AUTOMODEL_CONFIG=/tmp/amcfg/config.toml
export OPENROUTER_API_KEY="$(sed -n 's/^openrouter_api_key *= *"\(.*\)"/\1/p' ~/.config/automodel/config.toml)"
/tmp/am eval --catalog catalog.proposed.toml --split train --repeat 3 --summary
/tmp/am eval --catalog catalog.proposed.toml --split test  --repeat 3 --summary --check
```

A full run (271 cases × 3) costs about $0.03. `--json` gives every answer
(probabilities, confidence, continues and mode probabilities) for offline
analysis: decision rules, thresholds and penalties can be compared on saved
answers without asking Jev again.

## What to read

Per scope, for Jev's top level and for the router's decision:

| metric | why |
|---|---|
| exact accuracy | "acceptable" alone hid the collapse (86% acceptable, 79% exact) |
| mean absolute rank error | distance, not just hit or miss |
| recall per tier | a tier Jev or the policy skips shows here first (medium 62%) |
| confusion matrix | whether errors are adjacent (fine) or skip a tier (not) |
| decision share vs label share | the owner-visible symptom: "mostly low or xhigh" |
| below / above the label | the policy's bias (a high penalty pushes decisions up) |
| ECE of the top probability | whether confidence thresholds mean anything |
| cases whose top changed across runs | noise floor; compare variants above it |

The gap between Jev's top and the router's decision is the policy's doing
(penalty, warm gates, go-ahead handling), not Jev's.

## Regression gate

`--check` fails unless, on the main scope, decision exact ≥ 88%, recall ≥
80% for every tier with 20+ answers, each tier's decision share within 6
points of its label share, and rank error ≤ 0.12 (`eval.DefaultGate`). Run
it on `--split test --repeat 3`. The 2026-09 router passes with 91% / 0.09;
the collapsed one scored 79% / 0.24 and the same code with the old penalty
fails on five counts.

## Writing criteria and questions (TypeSafe Score)

From the TypeSafe docs (Score, Advanced structure, jev-1.13 jaggedness) and
what the 2026-09 experiments measured:

- Each level is judged **on its own** against the state; Jev never sees
  level numbers or neighbours. Ordering words ("more than the previous
  level") do nothing. Describe situations, not degrees.
- Keep one dimension per Score. When a level mixes things ("big *and*
  risky"), split the question and combine in code (composite scoring).
- Jev reads literally: write the exact condition; put boundary cases in the
  criteria. A level can be an object (`what`, `examples`, `not_for`); use it
  when Jev keeps splitting two neighbours on inputs you think are clear, and
  keep examples generic (never copied from test cases).
- Name state fields by path in the instructions (`` `task` ``,
  `` `last_assistant` ``) when the question is about one part of the state.
- Keep the state lean: unrelated detail is a distractor.

Measured on jev-1.13 (train / held-out exact, same decision rule):
current plain criteria 84.3% / 92.6%; field-path instructions 84.3% /
90.1%; structured levels (`what`/`examples`/`not_for`) 83.6% / 94.7%;
composite of four dimension Scores + the level, fitted on train, 88.3% /
93.3%; dropping `current` from the state 82.6% / 92.6%. None won on both
splits: the plain criteria stayed. Re-test them when the benchmark grows or
Jev changes.

## Decision rule and penalty

With per-turn effort a switch is free, so the cost-aware rule reduces to the
tier with the lowest expected loss under `underprovision_penalty` κ. Compare
κ against Jev's argmax on both splits, on exact accuracy **and** on the
realized loss against the labels (Σ cost gap, × κ when below the label).
jev-1.13: κ = 3 cost 7 points of exact accuracy on held-out and gave xhigh
27% of decisions for 20% of labels; κ = 1.5 matched argmax's accuracy with
the lowest loss at every κ. Expected-level cutpoints fitted on train
overfit (90.6% → 89.0%, below argmax).

## Warm gates

The confidence gate (`features.warm_min_confidence`) and the no-downgrade
gate (`continues_threshold`) only guard switches that cost something (a
cache rebuild). On free per-turn effort changes they froze sessions (warm
cases 69% exact with the gates, 91% without). A bare go-ahead keeps the
tier without asking Jev (also after compaction or a pause), unless it
answers a proposal ("Want me to fix it?"), which is routed. The eval
mirrors these rules; keep it in sync with `internal/hooks/decide.go`.
