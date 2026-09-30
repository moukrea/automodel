# Evaluating routing decisions

How to measure whether Jev and the router pick the right tier, and how to
change criteria, questions and policy without fooling yourself. Background
and full numbers: `docs/research/2026-09-routing-quality.md`.

## The benchmark

`testdata/eval/routing.jsonl`, one case per line:

```json
{"id": "w-extend-test", "scope": "main", "warm": true,
 "state": {"phase": "warm", "task": "...", "work_in_progress": {"goal": "...", "level": "xhigh"},
           "recent_prompts": ["..."], "last_assistant": "...",
           "current": {"tier": "xhigh", "effort": "xhigh"}, "session": {...}, "repo": {...}},
 "want": "xhigh", "accept": ["xhigh"], "relation": "extend",
 "note": "A test for the race just fixed: part of that work.", "split": "test", "modes": {"ultracode": false}}
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
  A follow-up that continues, extends, informs or asks about the work in
  progress keeps that work's level, or more if it adds harder work: the
  effort runs the whole turn, which carries the pending work on ("yes, and
  add a test for that" after an xhigh race fix is xhigh; so is "is CI green
  yet?" while that fix is pending). A wrap-up (commit message, summary, PR
  description, push) or a separate new task gets its own level (a commit
  message after a race fix is low). A go-ahead takes on the work it
  approves. A question while the work is still pending is a side question
  at the work's level, even an unrelated one ("which command shows a
  folder's size?"); a question that only recalls or explains finished work
  is a wrap-up. The same change repeated on another target (another
  endpoint, page or module) is a new task; another case or input of the
  same deliverable extends it. A prompt typed while Claude works or sent by
  another session is labeled like a follow-up: it never lowers the work.
  An effort asked for in words ("passe en low") is the label, up or down;
  "ultrathink" is at least xhigh; "think harder" and its family one rank
  above the higher of the work's level and the tier in force. Going back to
  paused work ("back to the migration") takes that work's level.
- `relation` labels how a warm, resumed or post-compaction prompt relates
  to the work in progress: `continue` (go-ahead, keep going, resume),
  `extend` (adds to, constrains or corrects it), `inform` (a fact, a
  preference or an answer, no new work), `side_question` (a question or a
  check aside while the work stays pending), `resume` (goes back to the
  paused work, only with `state.paused_work`), `wrap_up` (summary, commit,
  PR, push, changelog or recap of finished work) or `new_task` (separate
  work). Label the relation from the conversation, not from the tier: the
  same words can be a side question while work is pending and a new task
  once it is done. `want` follows from it with the rubric above.
- `explicit` labels what the prompt asks for in words: `{"effort": "xhigh"}`
  (or `"more"` for "think harder" and its family; not "ultrathink", a
  keyword), `{"mode": "ultracode"}` (or `"off"`), `{"model": "sonnet"}`.
  A case without it asks for nothing, so a mention ("why did it stay at
  xhigh?", "max retries is 3", "Sonnet 5.5 is out", "why did it pick
  Opus?") has no `explicit` and counts against precision if Jev confirms
  it. A model request is scored only for a model other than the session's
  that a main session can run on (Haiku is the asked tier's question);
  `want` stays the level (the work runs on the model at that level), and
  the eval scores the model separately. Include mentions and refusals, in
  French and in English, and many model mentions of the kind this repo's
  own conversations are full of (release news, comparisons, benchmark
  talk, questions about routing, a subagent or a config set to a model):
  they are what the explicit questions must reject.
- `state.work_in_progress` (`{goal, level}`, the level an effort name) is
  the work in progress as the hooks send it; without it the eval takes
  `state.current`. It may also carry `mode` (else `state.current`'s) and
  `model` (the model a request in words moved the work to), which the eval
  uses but doesn't send, like the hooks. `state.paused_work` (`{goal,
  level}`, same extras) is the work a detour paused; with it Jev is offered
  `resume`. A session on a model outside the tiers has `state.current`
  `{effort, model}` (the model's label) instead of a tier.
  `state.mid_turn: true` marks a prompt typed while Claude was working (the
  eval applies the mid-turn rule; it is not sent to Jev). A message from
  another session starts with `<cross-session-message` or "Another Claude
  session sent a message".
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

A full run (432 cases × 3) costs about $0.10. `--json` gives every answer
(probabilities, confidence, relation, explicit-request and mode
probabilities) for offline analysis: decision rules, thresholds and
penalties are compared on saved answers without asking Jev again with
`automodel eval --catalog <variant> --answers <saved run> --summary` (it
re-judges; a change to a question's wording needs a live run).

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

And across scopes:

| metric | why |
|---|---|
| relation accuracy, confusion, ECE of the top probability | whether follow-ups and separate work are told apart, and whether `relation_separate_threshold` means anything |
| follow-ups below the work they hold | the owner's complaint: a continuation, an addition, a side question, a resume, a mid-turn or peer prompt that lowered the effort (asked efforts excluded) |
| explicit requests: precision, recall, per kind | a mention read as a request changes the effort, the mode or the model for nothing; a missed request is ignored |
| model the work runs on (router decision) | a model asked in words is kept for the work, and left on a new task |
| mode on/off (router decision) | ultracode kept, turned on or off where the labels say |

The gap between Jev's top and the router's decision is the policy's doing
(penalty, the work in progress, requests in words, go-ahead handling), not
Jev's.

## Regression gate

`--check` fails unless, on the main scope, decision exact ≥ 88%, recall ≥
80% for every tier with 20+ answers, each tier's decision share within 6
points of its label share, rank error ≤ 0.12, no decision below the
label on a case that holds the work (relation `continue`, `extend`,
`inform`, `side_question` or `resume`, a mid-turn prompt, a peer message),
no such decision below the work it holds (efforts asked in words aside),
and no effort, mode or model request confirmed where the label has none
(`eval.DefaultGate`). Run it on `--split test --repeat 3`; the last two
must hold on the train split too.
The 2026-09 router passes with 91% / 0.09; the collapsed one scored 79% /
0.24 and the same code with the old penalty fails on five counts.

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

## The work in progress and warm gates

A prompt gets its own level (below the work in progress if that is its
level) only when Jev's relation answer puts at least
`meta.relation_separate_threshold` on `new_task` + `wrap_up` and it didn't
arrive mid-turn or from another session; anything else keeps at least the
work's tier and mode. The floor is the work in progress, set when separate
work starts, never the last prompt's tier, so upgrades stay free: holding
the *current* tier froze sessions (warm cases 69% exact with the old gates,
91% without), and a low session given a hard new task must still go up.
Keep a regression case for it. Tune `relation_separate_threshold` on the
train split: the right relation's probability sits at 0.9+ on clear
prompts and near 0.5 on mixed ones; don't carry over a threshold tuned on
a Noul (TypeSafe: a Choice's probabilities are relative). Tune
`explicit_threshold` (effort, mode) and `explicit_model_threshold` in the
gap between requests and mentions; the model one stays at least as strict.

A new task below the work in progress pauses that work (`paused_work`,
two hours); `resume` restores it. A model asked in words
(`work_in_progress.model`) runs the work, not the session: follow-ups and
wrap-ups stay on it, a new task goes back to the tiers.

The confidence gate (`features.warm_min_confidence`) only guards
downgrades that cost something (a cache rebuild). A bare go-ahead brings
back the work in progress's tier and mode without asking Jev (also after a
compaction or a pause), unless it answers a proposal ("Want me to fix
it?"), which is routed with the work as a floor. The eval mirrors these
rules (`internal/eval` `setup`, `router.Judge`); keep it in sync with
`internal/hooks/decide.go`.
