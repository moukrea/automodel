# Warm routing v2 (2026-09)

The owner's review of automodel's warm turns: follow-ups of hard work
dropped the effort in the middle of the work, and requests written in prose
("do this at xhigh", "use ultracode for this") were ignored, while the
owner also required that *talking* about a model, an effort or ultracode
never changes anything. This document gives the evidence, the design, the
evaluation before and after, the thresholds and what is still open. All
runs use `typesafe/jev-1.13`, 3 calls per case. Every prompt quoted here is
an invented paraphrase; no real prompt is reproduced.

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
numbers are aggregates only; no per-case test row was read while tuning.

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

**Held out (185 cases, two runs of 3, identical aggregates)**

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

**Regression gate** (`--split test --repeat 3 --check`, run twice): the two
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
