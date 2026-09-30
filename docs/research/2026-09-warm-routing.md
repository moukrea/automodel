# Warm routing v2 (2026-09)

The owner's review of automodel's warm turns: follow-ups of hard work
dropped the effort in the middle of the work, and requests written in prose
("do this at xhigh", "use ultracode for this") were ignored, while the
owner also required that *talking* about a model, an effort or ultracode
never changes anything. This document gives the evidence, the design, the
evaluation before and after, the thresholds and what is still open. All
runs use `typesafe/jev-1.13`, 3 calls per case. Sections 1 to 6 describe
the first round; section 7 the review that followed, its fixes and the
retune, and it corrects §4: the first round's test split was not held out.
Section 8 is round 3: the first fresh held-out run (79% exact), what it
found and the fixes. Every prompt quoted here is an invented paraphrase;
no real prompt is reproduced.

## 1. The problem

### Ledger forensics (9 real sessions, 2026-09-27 to 09-30)

The ledger's 594 decisions (412 main scope) were joined with the routing
states sent to Jev and with the transcripts (turn ends, queued prompts,
messages from other sessions). The 9 real sessions held 218 main decisions,
175 of them on a warm cache: 131 typed by the user, 39 messages from other
Claude sessions and 5 scheduled wakeups.

- **Every real warm downgrade was a free switch.** All 41 warm tier
  downgrades were per-turn effort changes on Opus 5.5, which cost nothing,
  and the router's `case free:` let Jev's pick through before the
  confidence and "continues the work" gates. Before the change that made
  free switches unconditional, 1 of 17 warm turns went down; after it, 40
  of 158.
- **Most of them were wrong.** Of the 36 downgrades that could be judged,
  20 were wrong (56%; 62% of the typed ones), 16 right, 5 debatable. That is
  11% of all real warm turns.

| class of prompt | downgrades | wrong | right | debatable |
|---|---:|---:|---:|---:|
| go-ahead with extra words ("continue, the limits are reset") | 11 | 6 | 2 | 2 (+1 masked by the mode) |
| addition, constraint or correction ("and convert the prices to euros incl. VAT") | 11 | 8 | 2 | 1 |
| side question while work was pending ("so, how is it going?") | 5 | 5 | 0 | 0 |
| summary or status of finished work | 1 | 0 | 1 | 0 |
| genuinely new, smaller task | 11 | 0 | 9 | 2 |
| FYI only, or a stale wakeup | 3 | 1 | 2 | 0 |

- **The lower effort really ran**: after one xhigh→low decision, the next 48
  requests ran at low; after an xhigh→medium on a message from another
  session, 26 requests ran at medium in the middle of a deep investigation.
  The owner's next prompts after three of the wrong ones were complaints or
  a hand-pinned effort.
- **Jev was right about the words, wrong for the turn.** The level question
  rates the new prompt's own work; seven wrong downgrades had P(low) ≥ 0.85
  with confidence ≥ 0.75 ("is CI green?" *is* low work). But the effort
  governs the whole turn, which carries the pending work on.
- **The old "continues the work" probability did not separate them**:
  0.27–0.92 on the wrong downgrades, 0.17–0.90 on the right ones.
- **Prompts typed mid-turn and other sessions' messages**: 7 of the 20
  wrong downgrades arrived while Claude was still working; 44 of 175 warm
  decisions came from other sessions or wakeups, which the hook took for the
  user.
- **Requests in prose were not parsed.** Only `[effort:x]` / `[model:x]`
  tags and a narrow "think harder" regex acted. Two explicit ultracode
  requests got a suitability yes-probability of 0.45 (threshold 0.75), so
  the mode stayed off; "look into it in depth" matched nothing.
- Cost of the fix, estimated from the same sessions: about $0.5 of extra
  output for the downgrades it blocks, against $8.6 "saved" by the wrong
  ones.

### The eval could not see it

`testdata/eval/routing.jsonl` encoded the behaviour the owner rejects: its
rubric said "a follow-up does not inherit the difficulty of the earlier
work", 11 cases labelled as continuing the work had a label below the
current tier, side questions while work was pending were all labelled low,
and no case asked for an effort, a mode or a model in words. On its warm
cases the pre-v2 router scored 79.2% exact (100% acceptable: every downgrade
was inside `accept`) on train and 89.4% / 95.7% on test; on 50 new warm
cases written for the review it scored 75% exact, with 30 of 150 answers
below the label and ultracode turned on 0 times out of 9 requests.

## 2. The design

**The work in progress** (`state.Work`: tier, mode, model, goal, since) is
what a follow-up is measured against. The first decision sets it, and so do
a prompt that starts separate work and an effort, a mode or a model asked
for in words; a follow-up that needs more raises it; side questions,
wrap-ups, pins and compactions keep it.

**One relation Choice** replaces the two warm yes/no questions. Jev sees the
work's goal and level and answers how the prompt relates to it:

| relation | tier |
|---|---|
| `continue` (a go-ahead, or carrying out what the assistant proposed) | at least the work's |
| `extend` (an addition, a constraint, a correction) | at least the work's |
| `inform` (a fact, a preference, an answer) | at least the work's |
| `side_question` (anything that only needs an answer while the work is pending) | at least the work's |
| `resume` (back to paused work; offered only when there is some) | the paused work's |
| `wrap_up` (commit, PR, push, summary, a question about finished work) | its own |
| `new_task` (separate work, the same change on another target included) | its own; it becomes the work |

A prompt gets its own level only when P(new_task) + P(wrap_up) reaches
`meta.relation_separate_threshold`, and never when it arrived mid-turn
(no turn end after the last tool call) or from another session. Upgrades
stay free: the floor is the work, never the last prompt's tier, so a low
session given hard work goes up at once. Options are objects (`what`,
`not_for`, `examples`, TypeSafe's advanced structure), in English with
French examples.

**Paused work.** A new task *below* the work in progress is often a detour
("quick one, fix the typo in the README" in the middle of a migration).
The work it replaces is kept as `Session.Paused` for two hours; while it is
there Jev sees `paused_work {goal, level}` and is offered `resume`; resume
restores its tier, mode, model and goal and holds at it. Another new task
drops it.

**Requests in words.** A regex only *pre-filters*: it finds the words that
may make a request (effort names, the "think harder" family, ultracode,
workflows, parallel agents and their refusals, the names of models other
than the session's) and adds one yes/no per candidate to the same Jev call.
Nothing acts on the regex alone except Claude Code's own `ultrathink`
keyword (a floor at xhigh). A confirmed effort is applied up or down, a
confirmed ultracode turns the mode on or off, "think harder" raises the
floor one tier above the work.

**A model asked in words runs the work, not the session.** The model
question asks whether the prompt wants *the assistant itself* to run on
that model for this work; its no side names what this repository's own
conversations are full of: mentions, comparisons, release news, prices,
benchmarks, questions about models or about how automodel routed, a
subagent or a config set to a model, refusals. It needs a stricter
threshold than efforts and modes. Once confirmed, the model becomes the
work's (`Work.Model`): routing goes on (Jev is still asked), follow-ups and
wrap-ups run on it at the effort routing picks (its default when the model
lacks it), and a separate new task, or asking for the tiers' own model,
goes back to the tiers. Only the `[model:x]` tag pins for good; `[effort:x]`
and `/effort` on the work's model pin it there. Haiku is never a candidate
(it is the asked tier's question), nor is the session's own model.

Also: a go-ahead restores the work's tier and mode (a go-ahead to a proposal
is routed with the work as a floor); compactions keep the work and the
decision on the summary can't go below it; pins keep ultracode unless their
effort is below it; the ledger and `automodel why` record the relation, the
requests, the hold, and work paused or resumed.

## 3. The benchmark

438 cases (253 train, 185 test), all invented:

- the 317 existing cases relabelled with the new rubric (a follow-up keeps
  the work's level; a wrap-up or a new task gets its own): 22 labels moved,
  24 lost the tiers below the work from `accept`;
- 99 new warm cases (go-aheads in and out of the fast-path list, go-aheads
  with an addition, corrections, side questions deep, shallow and
  unrelated, wrap-ups, new tasks, mid-turn prompts, peer messages, ultracode
  on, and effort, mode and model requests with their mentions and refusals,
  EN and FR);
- 16 cases for the fixes: model news, comparisons, benchmark talk, "why did
  the reviewer go to Sonnet?", a subagent set to Sonnet, refusals; requests
  for Sonnet, Fable and back to Opus; a follow-up of work running on Sonnet;
  back to paused work after a detour, and a follow-up of the detour;
- 6 train cases contrasting questions about finished work (wrap-up) with
  the same questions while the work is pending (side question).

New metrics: relation accuracy, confusion and ECE; explicit precision and
recall per kind (a model request only counts for a model other than the
session's that a main session can run); the model the decision runs on;
follow-ups decided below the work they hold (continue, extend, inform,
side_question, resume, mid-turn, peer; efforts asked in words aside).
New gate conditions: no request confirmed on a prompt that makes none, per
kind, and no follow-up below its work. `automodel eval --answers` re-judges a
saved run under another catalog without asking Jev, which is how the
thresholds below were compared.

## 4. Before and after

"Before" is the v2 router as first implemented (untuned wording, thresholds
0.6 / 0.8, a model asked in words pinned) on the same 438 cases. Test
numbers are aggregates only. **Correction (§7):** they are no held-out
evidence: the split was per prompt, so 43 of the 110 relation-labelled test
cases shared a conversation with train cases, and the test relation
confusion (wrap-ups read as side questions) was read before six train cases
and three wording changes aimed at it.

**Train (253 cases, 3 runs)**

| main scope | before | after |
|---|---:|---:|
| router decision exact / acceptable | 84.2% / 93.9% | 87.7% / 97.5% |
| rank error | 0.21 | 0.14 |
| decisions below / above the label | 33 / 70 | 24 / 56 |
| recall low / medium / high / xhigh / max | 76 / 80 / 77 / 96 / 75% | 87 / 82 / 79 / 99 / 75% |
| relation right (ECE) | 77.2% (0.14) | 86.1% (0.04) |
| follow-ups below their work | 6 / 240 | 0 / 252 |
| explicit requests: right / false / missed | 24 / 0 / 24 [^1] | 42 / 0 / 0 |
| ultracode on/off (router decision) | 627 / 645 | 636 / 645 |

Relation confusion after (train, rows = label): continue 99 right, 18 read
as extend (both hold, no effect on the tier); new_task 52, 9 read as
extend, 2 as continue; wrap_up 51, 3 read as new_task; side_question 39/39;
resume 3/3; inform 21, 9 read as extend.

**Test split (185 cases, two runs of 3, identical aggregates; not held out, see §7)**

| main scope | before | after |
|---|---:|---:|
| router decision exact / acceptable | 85.4% / 90% | 87.4% / 92% |
| rank error | 0.25 | 0.19 |
| decisions below / above the label | 19 / 52 | 12 / 49 |
| recall low / medium / high / xhigh / max | 57 / 93 / 83 / 97 / 80% | 62 / 93 / 86 / 98 / 80% |
| relation right (ECE) | 72% (0.15) | 82% (0.05) |
| follow-ups below their work | 8 / 201 | 0 / 210 |
| explicit requests: right / false / missed | 24 / 0 / 12 [^1] | 30 / 0 / 0 (effort 18, mode 9, model 3) |
| the model the work runs on | – | 6 / 6 |
| ultracode on/off (router decision) | 463 / 477 | 466 / 477 |

[^1]: The untuned eval also counted as missed the requests for the
    session's own model and for Haiku, which the router never asks about;
    the mode requests it missed are real misses (0.60–0.73 < 0.8).

**Regression gate** (`--split test --repeat 3 --check`, run twice; the test split was not held out): the two
owner rules pass on both splits: no effort, mode or model request confirmed
on a prompt that makes none, and no follow-up decided below its work. The
gate still fails on decision exact (87.4% < 88%), rank error (0.19 > 0.12),
low recall (62% < 80%) and 6 decisions below the label on follow-ups; the
untuned router failed the same four, further off. What remains:

- *Follow-ups labelled above their work*: a follow-up labelled max after
  xhigh work, or xhigh after high, where Jev's level stays at the work's.
  The hold can't raise a tier; the level question would have to.
- *Prompts held at the work that the labels give their own level* (the
  low→high and low→xhigh cells): on test, a third of the wrap-ups are read
  as side questions. Six of the seven "question about finished work"
  wrap-ups ended up in the test split during the relabelling and one in
  train, which is why train shows no such confusion.
- The gate's exact and rank-error bars were set on the pre-v2 benchmark,
  where follow-ups were labelled at their own level; a rubric that holds
  follow-ups at the work trades some over-provisioning for never lowering
  them. Changing `eval.DefaultGate` is the owner's call.

## 5. Thresholds and why

All from the train split; the same answers were re-judged at each value.
Superseded by §7 (0.55 / 0.8 / 0.85 on the conversation split).

- **`relation_separate_threshold` = 0.5** (was 0.6). Follow-ups put at most
  0.42 on new_task + wrap_up. On the same answers, 0.45 and 0.5 give
  decision exact 88.0% and rank error 0.14 with no follow-up lowered; 0.6
  gives 87.4% (four separate prompts held at the work); 0.4 lowers one
  follow-up. 0.5 reads as "separate once Jev finds it likelier than not",
  with a 0.08 margin over the highest follow-up. With the first wording the
  gap sat higher (follow-ups up to 0.53, separate prompts from 0.66) and
  0.6 was right there; the wording that fixed the confusions moved it.
- **`explicit_threshold` = 0.6** (was 0.8). Effort and mode requests scored
  0.72–0.98, the lowest a request for parallel agents that doesn't say
  "ultracode"; mentions and refusals 0.01–0.05. With the first wording the
  mode requests scored 0.60–0.70 and 0.8 missed every one; naming it "the
  ultracode mode" raised them. (A refusal written "plus besoin
  d'ultracode" was not even a candidate: the refusal pattern now knows it.)
- **`explicit_model_threshold` = 0.75** (spec default 0.9). Model requests
  0.81–0.93 ("switch back to Opus for the risky part" is the lowest);
  mentions, news, comparisons, benchmark talk, routing questions, a
  subagent's model and refusals 0.04–0.10. At 0.9 half the train requests
  were missed; 0.75 stays stricter than efforts and modes with a 0.65
  margin over the mentions. No mention was confirmed at any threshold
  tried, on either split.
- Unchanged, still holding on the relabelled set: the Haiku asked tier at
  0.92 (Haiku cases ≥ 0.95; the only other cases above 0.92 are warm, where
  Haiku is never taken, or have Haiku in `accept`); ultracode suitability at
  0.75 (yes-cases from 0.50, no-cases at most 0.73 but for three: an xhigh
  auth case at 0.84, a refusal of parallel agents at 0.88, which the
  explicit question turns off, and a trivial new task in a session already
  in ultracode at 0.92, where the mode stays on). No threshold does better
  on either side.

Wording changes kept (each won on train, 3 runs): `side_question` covers any
question that only needs an answer while the work is pending, unrelated
ones too, and not questions about finished work (they are `wrap_up`);
`new_task` covers the same change repeated on another target; `continue`
covers carrying out what the assistant proposed (a go-ahead to a detailed
proposal had put 0.5 on new_task). Relation accuracy went from 78.8% to
86.1% and its ECE from 0.11 to 0.04; two unrelated questions asked while
work was pending, read as new tasks and decided below the work with the
first wording, are held.

## 6. Open limits

As of the first round; §7 says which the review closed (the live check was
run, questions unrelated to the work are now asides, a small new task no
longer inherits ultracode through the turn-off hysteresis).

- The live check in real Claude Code sessions (spec §8) is not done yet.
- Recaps of finished work read as side questions (held at the work) on the
  held-out split; train has only one of them. Grow train from real
  misroutes of that kind (`automodel flag`) before rewording again.
- The ultracode suitability question reads the whole work in the state: a
  trivial new task in an ultracode session can keep the mode (0.92 on one
  train case), and the tier with it.
- Fuzzy boundaries stay fuzzy: the same pattern on another target vs
  another input of the same deliverable (new task or extension); a
  recap vs a question about a detail while work is pending.
- A model asked in words that has no main tier runs "off the tiers": the
  decision is stored like a model pin (`tier = "pinned"`, no pin set), so
  Jev sees `current {effort, model}` instead of a tier.
- Prose model requests are few (two cases on train, one on test), so model
  recall rests on little; precision rests on four mention cases per split
  (news, comparisons, benchmark talk, routing questions, a subagent's model,
  refusals), none ever confirmed.
- Messages marked `[jaunt bridge]` are not recognised as peer messages.

## 7. Round 2: the review, the fixes, the retune (2026-09-30)

Four reviews followed the first round: the eval, the routing code, its
robustness, and live Claude Code sessions on an isolated harness (22 of 25
live checks passed; the applied effort matched the decision on all 68
routed requests).

### What they found

- **A mention confirmed as a request, the critical one.** With the xhigh
  deadlock hunt pending, "set it to low in the tuning file for the
  subagents, then keep going" and "use low for the explore agents in the
  workflow" got 0.84–0.89 on the effort question: the decision *and the
  work in progress* went to low, and every later go-ahead restored low.
  The benchmark had no negative low, medium, "more" or mode-off candidate,
  so it reported 100% precision.
- **The gate could not pass.** The follow-up check compared the decision to
  the exact label: a bare "continue" after a compaction, labelled max on
  xhigh work with xhigh acceptable, is always decided at the work's xhigh.
- **The test split was not held out** (the correction in §4).
- **Detours.** After "quick one: fix the typo", a bare "ok, continue" took
  the fast path and went on at the typo's level with the mode off; the
  paused migration was never resumed.
- An effort asked in words turned ultracode off and kept Jev from turning
  it on; an effort asked for a commit message lowered the work; a small new
  task in an ultracode session inherited the mode as its own work; the
  regex missed "set the effort to medium", "mets l'effort à low",
  "effort élevé", "skip ultracode", "in parallel"; resume needed no minimum
  probability; a Jev failure ignored the budget cap and `disable_modes`;
  leaving a model asked for in words skipped switch costs and the
  confidence gate; a prompt quoting the peer phrase was taken for a peer
  message; a remark typed early in a turn was not seen as mid-turn (Claude
  Code writes the turn's entries seconds later); unrelated trivial
  questions were always held at the work's level.

### The fixes

| area | change |
|---|---|
| explicit questions | about the effort, mode or model the assistant *itself* uses for its own work (this prompt or the rest of the work); "no" names subagents, workflow stages, configs, tunings, quoted prompts, test strings |
| thresholds | `explicit_threshold` 0.8, `explicit_model_threshold` 0.85; an effort below the work needs 0.9 |
| requests and the work | an effort or mode asked for a wrap-up, a side question or an aside is for that turn; an effort keeps the work's mode, and Jev may turn one on unless it would raise that effort |
| regex pre-filter | levels near effort / niveau / reasoning, French level words, English mode words; any mode word asks the mode-off question too |
| metadata privacy | only "think harder / more / deeply", "réfléchis bien / plus / davantage / en profondeur", "prends ton temps" count without Jev, for that turn only |
| relations | new `aside` (unrelated: its own level, that turn only); `side_question` about the work or how the session runs it |
| done work | a wrap-up marks the work done; a question or a fact then gets its own level; a go-ahead or an addition reopens it |
| detours | a go-ahead carries the higher of the work and the paused work, and resumes the paused one; so does a go-ahead to a proposal; resume needs `relation_separate_threshold`; a smaller task during a detour keeps the paused work |
| modes | no turn-off hysteresis for new tasks (kept for follow-ups and wrap-ups) |
| mid-turn, peers | a real prompt starts a turn unless it is the one being decided; the peer phrase only at the start |
| costs and caps | fallbacks and go-aheads within the budget cap, the repo's bounds and `disable_modes`; switch costs on the model the decision ends up on; leaving a model asked in words needs `warm_min_confidence`; `/effort` on it drops a mode above the effort; the report counts carried go-aheads as switches |
| eval | uses the hooks' go-ahead path (`router.Carried`); follow-ups checked against the lowest acceptable tier; ultracode on/off in the gate |

### The benchmark

- Relabelled: the four "continue" cases labelled max after xhigh work want
  xhigh (max acceptable); fixing one finding of an audit that asked for no
  fixes is a new task; unrelated questions while work is pending are
  asides; questions about how the session runs (why it stayed at xhigh,
  the status line) are side questions; 17 cases where the rubric is
  ambiguous (a small mechanical follow-up, implementing an agreed design)
  have their old lower tiers back in `accept`; 14 cases after a wrap-up
  mark the work done.
- 87 new cases, all invented: efforts, modes and models talked about or
  set for something else (subagents, workflow stages, tenant configs,
  tuning files, fixtures, quoted prompts, news, questions about the
  routing), Opus mentioned on sessions running Sonnet, asides against
  side questions, done work, detours ended by a go-ahead, prompts typed
  early in a turn, and the requests the wider regex finds, in English and
  French. Some come from the reviews' probes.
- Re-split by conversation: cases sharing a work goal, a paused work, the
  last assistant message or recent prompts go to the same split, label
  shares balanced: 315 train, 210 test. The new test split still holds the
  first round's test cases, so it is not held out either; it was not run.

### Results (train split, 3 runs each)

| main scope | first run | final (two runs) |
|---|---:|---:|
| router decision exact / acceptable | 87.6% / 93.4% | 90.9–91.0% / 97.1% |
| rank error | 0.19 | 0.10–0.11 |
| decisions below / above the label | 35 / 68 | 15 / 60–61 |
| recall low / medium / high / xhigh / max | 86 / 88 / 82 / 94 / 80% | 91 / 88 / 84–85 / 98 / 100% |
| follow-ups below their work | 17 / 378 | 0 / 372 |
| relation right (ECE) | 80.5% (0.06) | 85.8–87.3% (0.04) |
| explicit: right / false / missed | 37 / 0 / 23 | 48 / 0 / 12 |
| per kind (right / missed): effort, mode, model | 27/9, 6/9, 4/5 | 30/6, 9–10/5–6, 8–9/0–1 |
| ultracode on/off (on side, off side) | 816/831 (33/39, 783/792) | 819–820/831 (36–37/39, 783/792) |
| model the work runs on | 12/17 | 12/13, 12/12 |

"First run" is the fixed code with the reworded explicit questions and the
first round's relation wording, at 0.5 / 0.8 / 0.85. Relation confusion
(final, one run, rows = label): continue 81, 15 read as extend, 6 as a side
question; extend 109, 12 read as new_task (a detour's second fix, an
effort-lowering rename, work after a wrap-up), 5 as continue; inform 27,
15 read as extend; side_question 57, 3 as wrap_up; aside 27, 3 as
wrap_up; resume 12/12; wrap_up 72, 3 as side_question; new_task 73, 8 read
as extend, 3 as continue. Every relation confusion left is between two
holds or two separate relations, or held a separate prompt at the work;
none lowered a follow-up.

Wording changes kept, each after a 3-run comparison on train: the explicit
questions' "own work (this prompt, or the rest of the work in progress)"
and the yes examples (model recall 44% → 89–100%); `aside`'s `not_for`
(questions about the work, and prompts that also say to go on or how) and
`side_question`'s "how this session runs it" (follow-ups below their work
17 → 1); `continue`, `extend` and `resume` as in the catalog history
(resumes 0.47–0.59 → 0.75+, the last follow-up below its work gone).

### Thresholds

- `relation_separate_threshold` = 0.55: on the same answers 0.45 to 0.55
  (0.6 on one run) give identical decisions; 0.4 lowers two or three
  follow-ups; 0.65 and up hold separate prompts. Resumes need it too and
  sit at 0.75 or more.
- `explicit_threshold` = 0.8: efforts asked in words 0.82–0.98; mentions,
  efforts set for something else and refusals at most 0.20 (mode 0.22).
  Misses: lowerings Jev is less sure of, left to the 0.9 bar ("switch to
  medium for the remaining test rewrites" 0.85–0.87, "effort élevé, pas
  plus" 0.47–0.55), and parallel agents asked or refused without naming
  ultracode (0.61–0.80). Precision first: the spec's minimum.
- `explicit_model_threshold` = 0.85: requests 0.83–0.93, mentions at most
  0.21; in the two final runs one answer (0.83) was missed.

### The gate

`eval.DefaultGate` (thresholds unchanged, plus the ultracode on/off check)
passes on the train split in both final runs. It has not been run on a
held-out set: the old test split is used up and the new test split holds
its cases; a fresh set, written apart, is to be run once with `--check`.

### Still open

- The held-out run above.
- The ultracode question reads the whole work in the state: a draft PR
  after an ultracode sweep gets 0.92–0.93 and runs at xhigh with the mode
  (3 of the 9 off-side errors per run; 3 more are a refusal of parallel
  agents the explicit question missed, 3 a single-area upgrade at 0.82).
- Parallel agents asked or refused without the word ultracode, and
  lowerings asked in words, are the explicit misses.
- Live sessions again for the detour, aside and done-work paths.

## 8. Round 3: the first fresh held-out run (2026-09-30)

A fresh held-out set was written apart from everything the tuning had
read: 128 invented cases (24 conversations and 6 first prompts, about 54
in French), from the spec, the fixes and the rubric only. It was run once
with round 2's final catalog, two runs of 3.

### What it found

The gate failed: decision exact 79% (78.9%, 78.4%), acceptable 89%, rank
error 0.36, 12 decisions below the label on follow-ups and 9 below the
work they follow up. Precision held: no effort, mode or model request was
confirmed on a prompt that makes none. Relation 79% right; ultracode on/off
367–368 of 384 (on 27/27, off 340–341/357).

The 29–30 failing cases, one by one:

| cause | cases | what happened |
|---|---:|---|
| ultracode on a turn-only prompt | 3 | an unrelated question ("capitale de l'Australie ?", "Python's flatMap?") and a wrap-up ("commit wave 2") in ultracode sessions ran at xhigh with the mode: the mode question reads the whole work (0.92–0.95) and the mode in force stayed on |
| routing questions read as side questions | 3 | "why did automodel keep this whole session at xhigh?", "why did it pick Opus for this session?", "why was the first answer on Opus and now Sonnet?" were held at the work's level (side_question 0.92–0.99) |
| go-ahead on a done work | 1 | "looks good." after an xhigh fix was wrapped up is on the go-ahead list: carried at xhigh |
| "think harder" | 1 | low work, "explain the touch chain again but think harder": the floor was medium, but Jev's level read the words as hard (low 0.55, xhigh 0.45) and the pick went to xhigh |
| a model asked for the rest | 1 | "fais le reste avec Sonnet, pas besoin d'Opus pour du CSS" on medium work: high on Sonnet (level medium 0.54, high 0.46) |
| a lowering asked in words | 1 | "passe en medium pour les formulaires simples qui restent" at 0.89–0.91, under the 0.9 bar in half the answers |
| labels | 9 | 6 warm asides labelled Haiku (a warm session never moves there: the decision, low, was right); two small standalone edits ("set the review subagent to effort medium", a README paragraph) labelled as extensions at the work's high; a resume labelled medium only |
| Jev's answers | 11 | "mets le paquet" confirmed at 0.45; model requests at 0.66–0.80 under the 0.85 bar; a refusal of ultracode for part of the work at 0.77–0.80; ultracode turned on at 0.75–0.78 on a resumed Terraform migration and a remark typed mid-turn; the refund script after the double-charge fix read as a new task (0.73–0.77) at medium; three cases decided one rank above the label, within `accept` (a fact raised by the level, two follow-ups rated higher) |

The eval's JSON had no relation answer on the go-ahead fast path, which
the diagnosis of the done-work case needed.

### The fixes

| item | change |
|---|---|
| turn-only prompts (B1) | a wrap-up, a side question or an aside never turns a mode on nor keeps it for that turn, and the tier isn't raised to the mode's; the work keeps its mode for the next follow-up. A mode asked in words for that turn still applies; a prompt typed mid-turn or sent by another session keeps the turn's |
| routing questions (B2) | `aside` covers questions about automodel's routing (why the session or a subagent got, kept or changed its effort, model or mode) and acknowledgements; `side_question` is about the work itself, which model or mode would suit it included |
| done work (B3) | a go-ahead on a done work skips the fast path and is routed: continue or extend reopen the work, anything else takes its own level |
| more thinking (B4) | on a follow-up, one rank above the work and the tier in force is the decision, not only a floor; on new work it stays a floor; asked for a turn-only prompt it is for that turn |
| a model asked in words (B5) | on a follow-up that adds no work, it keeps the work's level on that model |
| lowerings (B6) | the effort question's yes side says a lower effort for easier work, or a cap, is asked as much as a raise, usually with its reason; "mets le paquet" and "going all out" are more thinking |
| eval (B7) | on a fast-path prompt the relation is asked apart, recorded with `fast_path`, and used neither by the decision nor by the relation metrics |

Three wording changes came from train gaps once the set was merged:
`continue` says a question or a remark along with a go-ahead is still a
go-ahead (after B2, "why did it stay at xhigh? just curious, keep going"
read as an aside at 0.81–0.93 and was lowered); `continue`'s `not_for`
sends acknowledgements of done work to `aside`; `extend` covers a step the
work calls for, such as repairing what the problem it fixes left behind
(the refund script now holds the work's xhigh). A first `aside` wording
that named "the effort, the model or the mode" drew requests for an
effort or a model into asides; the kept wording asks about the routing's
past choices and names requests and facts about a subagent in `not_for`.

### The benchmark

The 128 held-out cases were read one by one, so they join the train split
with their ids (443 train, 210 test). Label fixes: the warm Haiku labels
become low with Haiku acceptable; the two standalone edits become new
tasks at their own level (low), the work's still acceptable; the resume
accepts medium and high. The round 3 rubric, on both splits: a side
question runs without the work's mode (3 cases), questions about
automodel's routing are asides (6 cases, 2 of them in the test split), an
acknowledgement of done work is an aside (2 cases). The test split was
not run.

### Results (train split, 3 runs each)

| main scope | before | final (two runs) |
|---|---:|---:|
| router decision exact / acceptable | 89.8% / 96.4% | 91.9–92.1% / 97.7–97.8% |
| rank error | 0.13 | 0.09 |
| decisions below / above the label | 21 / 103 | 15 / 81–83 |
| recall low / medium / high / xhigh / max | 88 / 86 / 84 / 98 / 93% | 91 / 88 / 87 / 98 / 100% |
| follow-ups below the label / their work | 6 / 3 of 588 | 0 / 0 of 585 |
| relation right (ECE) | 83.5% (0.03) | 86.2–86.6% (0.02–0.03) |
| explicit: right / false / missed | 101 / 0 / 28 | 109–110 / 0 / 19–20 |
| per kind (right / missed): effort, mode, model | 61/14, 22/8, 18/6 | 69/6, 22–23/7–8, 18/6 |
| ultracode on/off (on side, off side) | 1192/1215 (51/57, 1141/1158) | 1204–1205/1215 (54/57, 1150–1151/1158) |

"Before" is the fixed code (B1, B3, B4, B5, B7) with round 2's wording and
thresholds, on the merged set before the last six relabels. Relation
confusion (final, one run, rows = label): continue 128, 23 read as extend,
5 as aside; extend 192, 13 as new_task, 11 as continue; inform 46, 23 as
extend; side_question 78, 3 as aside, 3 as wrap_up; aside 87, 3 as a side
question; resume 21/21; wrap_up 94, 5 as a side question; new_task 105,
12 as extend. None lowered a follow-up.

The 128 former held-out cases now score 95.3–95.6% exact and 100%
acceptable, where they scored 79% held out: train numbers, since the
fixes, the wording and the labels came from them.

### Thresholds

- `relation_separate_threshold` stays 0.55: 0.45 to 0.55 keep every
  follow-up at its work and differ by one to three answers on wrap-ups
  and asides; 0.4 lowers a routing question ending in "just curious,
  carry on".
- `explicit_threshold` stays 0.8 and the lowering bar 0.9: lowerings for
  the rest of the work, asked with a reason, now read 0.93–0.96,
  "mets le paquet" 0.94; "effort élevé, pas plus" 0.63–0.67 is still
  missed. Mentions and refusals at most 0.16 (effort), 0.23 (mode).
- `explicit_model_threshold` stays 0.85: most requests 0.87 and up;
  "Run this on Sonnet, it's simple" (0.66–0.71) and "switch this to Opus
  for the tricky test" (0.76–0.80) are missed; mentions at most 0.21.
- `modes.ultracode.threshold` 0.75 → 0.8: new work Jev turns the mode on
  for reads 0.86 and up; the off-labelled cases above 0.75 read 0.73–0.78
  (a Terraform migration resumed across six environments, remarks typed
  during a form migration). 0.85 would also turn off a Moment.js removal
  (0.82–0.83), 0.01 under the lowest on-case.

### The gate

`eval.DefaultGate` (unchanged) passes on the train split in both final
runs. A second fresh held-out set is being written; its run is the next
held-out number.

### Still open

- Model requests Jev is unsure of, parallel agents asked or refused
  without naming ultracode, and caps ("effort élevé, pas plus") are the
  explicit misses.
- An effort asked for the rest of the work that Jev reads as an aside
  ("set the effort to medium for the rest", aside 0.43–0.54 in some
  answers) is applied to that turn only, not to the work.
- Live sessions for the turn-only mode and the done-work go-ahead.
