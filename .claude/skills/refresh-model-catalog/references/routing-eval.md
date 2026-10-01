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
  `haiku` is the label only on first prompts and cold or post-compaction
  cases: a warm session never moves to Haiku (its cache), so warm trivia
  is `low` with `haiku` in `accept`.
  A follow-up that continues, extends, informs or asks about the work in
  progress keeps that work's level, or more if it adds harder work: the
  effort runs the whole turn, which carries the pending work on ("yes, and
  add a test for that" after an xhigh race fix is xhigh; so is "is CI green
  yet?" while that fix is pending). A wrap-up (commit message, summary, PR
  description, push) or a separate new task gets its own level (a commit
  message after a race fix is low). A go-ahead takes on the work it
  approves, even with a question or a remark along ("why did it stay at
  xhigh? just curious, keep going" continues). A question while the work
  is still pending is a side question at the work's level when it is about
  that work itself: its progress, its code, its choices, its checks, or
  which model or mode would suit it ("is CI green?", "would Fable do better
  on this bug?"). A question about automodel's routing (why the session or
  a subagent got, kept or changed its effort, model or mode: "why did it
  stay at xhigh?", "why did the reviewer run on Sonnet?", the status line's
  level), or what models cost ("is Sonnet cheaper than Opus for this kind
  of refactor?"), is an aside, unless routing is what the work in progress
  builds or tunes; so is a question or a remark unrelated to the work ("which
  command shows a folder's size?", model news, a comment in passing, a
  thank-you): its own level, for that turn only. A question that only
  recalls or explains finished work is a wrap-up. Once a wrap-up has
  closed the work (`work_in_progress.done`), only more work on it (a
  go-ahead or an addition that reopens it) keeps its level; a question or
  a fact gets its own, and an acknowledgement ("looks good.", "ok merci",
  "nice, that was quick") is an aside at its own level, even when its
  words are a go-ahead's. The same change repeated on another target (another
  endpoint, page or module) is a new task; another case or input of the
  same deliverable extends it (one more file for the audit or the review in
  progress, "continue: check the importer the same way"), and so does a
  test of what the work added ("add a test that an expired coupon is
  refused", after the work that added expiry), and a step the work calls for, such
  as repairing what the bug it fixes left behind (a script refunding the
  double charges after the double-charge fix), or a regression the work
  caused ("since the refactor the minimap icons flicker, can you look at
  that too?"). A small standalone edit
  asked in passing ("while you're in the config, set the review agent's
  effort to medium") is a new task at its own level. A prompt typed while Claude works or sent by
  another session is labeled like a follow-up: it never lowers the work.
  An effort asked for in words ("passe en low") is the label, up or down
  (asked for a wrap-up, a side question or an aside, for that turn only);
  an effort set for something else (a subagent, a workflow stage, a
  config or tuning entry, a quoted prompt or a test string) is no request;
  "ultrathink" is at least xhigh; "think harder" and its family one rank
  above the higher of the work's level and the tier in force (on a
  follow-up exactly that, not the level the words "think harder" suggest,
  unless the prompt adds work that needs more: then that work's level);
  on a prompt that takes its own level (a new task, a first prompt, a
  wrap-up, an aside), one rank above that level, whatever the work it
  leaves ("new thing, take your time: add validation to the signup form"
  in an xhigh session is high).
  A model asked for the rest of the work as it stands ("do the rest with
  Sonnet, it's only CSS") keeps the work's level on that model; with more
  work to it (an extension), that work's level. Asked for a wrap-up, a
  side question or an aside, a model, an effort, more thinking or
  "ultrathink" is for that answer only: the work keeps its level and
  model. Going back to
  paused work ("back to the migration", or a bare go-ahead once the detour
  is done) takes that work's level. A go-ahead to a proposal after a
  detour is labelled by what was proposed: going back to the paused work
  is a resume ("yes" to "Shall I get back to the migration?"), a wrap-up
  step of the detour a wrap-up at its own level ("yes" to "Committed.
  Want me to push it?"), more of the detour a continue at the detour's
  level, also when a remark follows the offer. An acknowledgement ("ok",
  "perfect", "lgtm", "nickel", "parfait") right after such an offer
  accepts it, labelled the same. A go-ahead to a closing
  question that offers nothing of the detour ("Done. Anything else?",
  "Shall I carry on?", "Ça te va ?") goes back to the paused work, as a
  bare go-ahead does: a resume at that work's level.
  A fact about how a part of the work may run (the effort or the model a
  subagent can use for one step) is inform at the work's level; an
  instruction to set it in a file or a config is a new task or an
  extension. Where the rubric is ambiguous (a small
  mechanical follow-up, implementing an agreed design), keep the lower
  tier the prompt alone would get in `accept`.
  Modes (`modes.ultracode`): a follow-up keeps the work's mode, a new task
  gets its own (a small one in an ultracode session has none), and a
  wrap-up, a side question or an aside runs without it, whatever the
  work's (the work keeps it for the next follow-up), unless the prompt
  asks for it in words for that turn; a prompt typed mid-turn or sent by
  another session keeps the mode the turn runs with. A mode asked for in
  words wants the tier it runs at (xhigh for ultracode); on a follow-up
  of work below it, the work's level stays in `accept` (the mode metric
  scores a missed request). Once a wrap-up closed
  the work, its mode is no longer in force (a fact on a cold turn, or the
  decision after a compaction, gets the mode its own work needs).
- `relation` labels how a warm, resumed or post-compaction prompt relates
  to the work in progress: `continue` (go-ahead, keep going, resume),
  `extend` (adds to, constrains or corrects it), `inform` (a fact, a
  preference or an answer, no new work; facts about the work's
  environment, resources, schedule or people too: "the staging cluster
  only has 4 GPUs free until noon"), `side_question` (a question or a
  check about the work itself while it stays pending), `aside` (a question
  or a remark unrelated to the work, an acknowledgement, or a question
  about automodel's routing; no work of its own; an instruction to do or
  change something never is), `resume` (goes back to the paused work, only with
  `state.paused_work`), `wrap_up` (summary, commit, PR, push, changelog or
  recap of finished work, and a go-ahead to such a step the assistant
  proposed once the work is done: "yes" to "Want me to open the PR?") or
  `new_task` (separate work). Label the relation from the conversation, not from the tier: the
  same words can be a side question while work is pending and a new task
  once it is done. `want` follows from it with the rubric above.
- `explicit` labels what the prompt asks for in words: `{"effort": "xhigh"}`
  (or `"more"` for "think harder" and its family; not "ultrathink", a
  keyword), `{"mode": "ultracode"}` (or `"off"`), `{"model": "sonnet"}`.
  A case without it asks for nothing, so a mention ("why did it stay at
  xhigh?", "max retries is 3", "Sonnet 5.5 is out", "why did it pick
  Opus?"), and an effort, a mode or a model set for something else
  ("use low for the explore agents", a tenant config, a quoted prompt),
  have no `explicit` and count against precision if Jev confirms them. A model request is scored only for a model other than the session's
  that a main session can run on (Haiku is the asked tier's question);
  `want` stays the level (the work runs on the model at that level), and
  the eval scores the model separately. Include mentions and refusals, in
  French and in English, and many model mentions of the kind this repo's
  own conversations are full of (release news, comparisons, benchmark
  talk, questions about routing, a subagent or a config set to a model):
  they are what the explicit questions must reject.
- `state.work_in_progress` (`{goal, level}`, the level an effort name, and
  `done: true` once a wrap-up closed it) is the work in progress as the
  hooks send it; without it the eval takes `state.current`. It may also carry `mode` (else `state.current`'s) and
  `model` (the model a request in words moved the work to), which the eval
  uses but doesn't send, like the hooks. `state.paused_work` (`{goal,
  level}`, same extras) is the work a detour paused; with it Jev is offered
  `resume`. With `kept: true` it is a detour a bare go-ahead went back
  from, below the work in progress: the eval keeps it for the router but,
  like the hooks, neither sends it nor offers `resume`. A session on a model outside the tiers has `state.current`
  `{effort, model}` (the model's label) instead of a tier.
  `state.mid_turn: true` marks a prompt typed while Claude was working (the
  eval applies the mid-turn rule; it is not sent to Jev). A message from
  another session starts with `<cross-session-message` or "Another Claude
  session sent a message".
- `split`: `train` (tune on it) or `test`. A conversation stays in one
  split: cases that share a work goal, a paused work, the last assistant
  message or the recent prompts are split together (the relation depends
  mostly on that state), and labels stay balanced within each split.
  A split is held out only while nobody has read its cases or its
  per-case results while tuning: the 2026-09-30 test split holds cases
  that were (see `docs/research/2026-09-warm-routing.md` §7), so
  held-out numbers come from a fresh set written apart and run once. Once
  its failures have been analysed case by case, a held-out set is used
  up: it joins the train split (held-out run 1 did in round 3, §8,
  held-out run 2 in round 4, §10), and
  the next held-out number needs another fresh set.
- Never copy real prompts or transcripts: invent the text.

## Running it

Use a dev build and an isolated config: without `--catalog`, `automodel
eval` loads the configured catalog through the store, which saves it as the
last-good copy in the config's state dir. A `--catalog` candidate is never
saved (builds before v0.17 saved it too).

```sh
go build -o /tmp/am ./cmd/automodel
mkdir -p /tmp/amcfg && printf 'state_dir = "./state"\n' > /tmp/amcfg/config.toml
export AUTOMODEL_CONFIG=/tmp/amcfg/config.toml
export OPENROUTER_API_KEY="$(sed -n 's/^openrouter_api_key *= *"\(.*\)"/\1/p' ~/.config/automodel/config.toml)"
/tmp/am eval --catalog catalog.proposed.toml --split train --repeat 3 --summary --check
/tmp/am eval --catalog catalog.proposed.toml --cases heldout.jsonl --repeat 3 --summary --check   # once, at the end
```

A train run (640 cases × 3) costs about $0.25 and takes about two minutes.
`--json` gives every answer (probabilities, confidence, relation,
explicit-request and mode probabilities) for offline analysis; on a prompt
the hooks take without the relation question (a bare go-ahead, a go-ahead
to a proposal: `fast_path`) the relation is asked apart, only to diagnose,
and neither the decision nor the relation metrics use it: decision rules, thresholds and
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
| follow-ups below the work they hold | the owner's complaint: a continuation, an addition, a side question, a resume, a mid-turn or peer prompt that lowered the effort (asked efforts excluded; after a wrap-up, only continue, extend and resume hold) |
| explicit requests: precision, recall, per kind | a mention read as a request changes the effort, the mode or the model for nothing; a missed request is ignored |
| model the work runs on (router decision) | a model asked in words is kept for the work, and left on a new task |
| mode on/off (router decision) | ultracode kept, turned on or off where the labels say, on the cases labeled on and on those labeled off |

The gap between Jev's top and the router's decision is the policy's doing
(penalty, the work in progress, requests in words, go-ahead handling), not
Jev's.

## Regression gate

`--check` fails unless, on the main scope, decision exact ≥ 88%, recall ≥
80% for every tier with 20+ answers, each tier's decision share within 6
points of its label share, rank error ≤ 0.12, the ultracode on/off
decision right on 80% of the cases labeled on and of those labeled off
(20+ answers each), no decision below every acceptable tier on a case
that holds the work (relation `continue`, `extend`, `inform`,
`side_question` or `resume`, a mid-turn prompt, a peer message), no such
decision below the work it holds (efforts asked in words aside), and no
effort, mode or model request confirmed where the label has none
(`eval.DefaultGate`; its thresholds change only with the owner's
approval). Run it on the train split while tuning (`--repeat 3`), and
once on a fresh held-out set at the end.
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
`meta.relation_separate_threshold` on `new_task` + `wrap_up` + `aside` and
it didn't arrive mid-turn or from another session; anything else keeps at
least the work's tier and mode. `resume` needs the same threshold on its
own. After a wrap-up (the work is done), a question or a fact gets its own
level too. The floor is the work in progress, set when separate
work starts, never the last prompt's tier, so upgrades stay free: holding
the *current* tier froze sessions (warm cases 69% exact with the old gates,
91% without), and a low session given a hard new task must still go up.
Keep a regression case for it. Tune `relation_separate_threshold` on the
train split: the right relation's probability sits at 0.9+ on clear
prompts and near 0.5 on mixed ones; don't carry over a threshold tuned on
a Noul (TypeSafe: a Choice's probabilities are relative). Tune
`explicit_threshold` (effort, mode; at least 0.8) and
`explicit_model_threshold` (at least 0.85) in the gap between requests and
mentions, precision first: a mention, or an effort set for a subagent or a
config, must never count. An effort below the work in progress needs 0.9
(`router.LowerEffortP`) whatever the threshold.

A new task below the work in progress pauses that work (`paused_work`,
two hours); `resume` restores it, and so does a bare go-ahead when the
paused work needs more, also once the detour was wrapped up ("commit
that", then "vas-y"); typed mid-turn, while the detour runs, a go-ahead
goes on with the turn. A further task below it keeps it paused. A model asked in words
(`work_in_progress.model`) runs the work, not the session: follow-ups and
wrap-ups stay on it, a new task goes back to the tiers (when the
confidence gate keeps that turn on the model, the new work is still on
the tiers).

The confidence gate (`features.warm_min_confidence`) only guards
downgrades that cost something (a cache rebuild), and leaving a model a
work runs on because it was asked in words; switch costs are those of the
model the decision ends up on. A bare go-ahead brings
back the work in progress's tier and mode without asking Jev (also after a
compaction or a pause), unless it answers a proposal (a question or an
offer at the end of the assistant's message, `router.Proposes`: "Want me
to fix it?", "Should I push it? CI takes ten minutes.", "let me know if
you want it"), which is routed with the work as a floor. After a detour
it is asked the relation question: the proposal may be to go back to the
paused work, to wrap the detour up or to do more of it; when the paused
work needs more, the go-ahead goes back to it unless the offer question
(`[questions.offer]`) says the assistant offered one more thing for the
detour, from `meta.detour_offer_threshold` (tune it in the gap the eval
prints as "detour offer": offers of the detour against closing questions
and offers to go back); a go-ahead that stays on the detour runs at the
detour's level, whatever the relation reads (Jev's level of the bare words
leans on the paused work, and the offer may be a wrap-up step or more of
the detour), and doesn't close it. Going back on a bare go-ahead (the
offer question under its bar, or Jev reading resume), an open detour
waits in turn (`Kept`): the next wrap-up closes it, not the work, at the
higher of the detour's level and Jev's at most. A kept detour is never
shown to Jev nor resumed: a go-ahead goes on with the work, and going
back to it in words is new work. Or the work is done and
no paused work needs more (`router.Acknowledges`): then it is routed like
any prompt, and Jev's relation says whether it reopens the work or only
acknowledges it; a go-ahead to a proposal there (or any go-ahead right
after a compaction) holds the work's level unless it is a wrap-up step or
an aside, and a bare go-ahead read as new work is answered alone, never a
work named "yes". The eval mirrors these
rules (`internal/eval` `setup`, `router.Judge`, and the hooks' own
go-ahead path `router.Carried`); keep it in sync with
`internal/hooks/decide.go`.
