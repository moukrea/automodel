# Routing quality (2026-09)

The owner's review of automodel's routing: *"it mostly picks low or xhigh, a
bit of high, almost never medium"*. This document measures it, finds where
it comes from, and records every variant tried, with train and held-out
numbers. All runs use `typesafe/jev-1.13` (build `jev-1.13-20260917`), 3
calls per case unless stated. Jev answers vary little: the top level changed
across the 3 runs on about 1% of cases.

## 1. The evidence, re-read

The ledger held 98 main decisions with Jev answers: low 42, medium 7,
high 8, xhigh 34, plus 5 from the old ultracode tier. Grouping them by the
project each session ran in (from the transcript path only; no prompt text
was read):

| project | decisions | low | medium | high | xhigh | ultracode |
|---|---:|---:|---:|---:|---:|---:|
| scripted demo (`demo-bookshop`) + scratch tests | 77 | 38 | 5 | 0 | 29 | 5 |
| organic sessions (3 repositories) | 21 | 4 | 2 | 10 | 5 | 0 |

The demo scripts send designed extremes ("What does HTTP status 409 mean?",
"write the commit message", the oversell race), so most of the low/xhigh
split came from them. The organic sessions lean on high, and medium is still
rare (2 of 21). Without labels those 21 decisions can't be scored, so the
work below uses a labeled benchmark.

## 2. The benchmark

`testdata/eval/routing.jsonl`: 271 invented cases (none copied from real
prompts), each with a `note` justifying its label and an `accept` set for
honest ambiguity.

| scope | cases | low | medium | high | xhigh | max | held out (`split: test`) |
|---|---:|---:|---:|---:|---:|---:|---:|
| main | 236 | 55 | 65 | 58 | 48 | 10 | 94 |

| scope | cases | haiku | opus-low | opus-medium | opus-high | opus-xhigh | opus-max | held out |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| subagent | 35 | 9 | 4 | 8 | 5 | 8 | 1 | 11 |

Main cases by phase: initial 139, warm 76, post-compaction 14, resumed 7.
They include short follow-ups, go-aheads ("yes", "continue"), French
prompts, long pasted errors, specs and logs (80–400 words), and warm turns
whose difficulty only shows in `recent_prompts` / `last_assistant`. They
also include simple follow-ups inside dense technical context (a 300–900
word assistant report on a race fix, then "push it"), and Claude Code
compaction summaries (700–1800 words). The 56 original cases, on which the
criteria had been tuned, are all in train. The new ones alternate
train/test within each label.

The label rubric is in `.claude/skills/refresh-model-catalog/references/routing-eval.md`.
In short: low needs no reasoning beyond recall or a specified mechanical edit;
medium is routine coding with a clear path in one area; high spans
components, involves design, or has a reproducible unknown cause; xhigh is
where a subtle mistake is expensive; max is exceptional. A follow-up doesn't
inherit the difficulty of the earlier work, and a go-ahead takes on the work
it approves.

## 3. New metrics

`automodel eval` used to print only "acceptable" and "router decision
acceptable". On the old 56 cases it read 100%, while the same router
decided medium work as high 25–31% of the time on the new cases. It now
prints, per scope, for Jev's top level and for the router's decision:
- exact accuracy and mean absolute rank error;
- recall per tier, and decisions below/above the label;
- both confusion matrices, and decision share against label share;
- mean p(label) and the ECE of the top probability.

It also takes `--repeat N`, `--split train|test` and `--check`, the
regression gate (section 9).

## 4. Baseline

Current catalog and code (penalty 3.0, warm gates, v2 features on).

### Held-out (94 main cases × 3)

| | Jev top | router decision |
|---|---:|---:|
| exact | 92% | **79%** |
| acceptable | 98% | 84% |
| mean abs rank error | 0.08 | 0.24 |

Decisions below the label 11, above 49. Mean p(label) 0.85, mean top p
0.89, ECE 0.04.

| tier | labels | label share | top share | decision share | recall (top) | recall (decision) |
|---|---:|---:|---:|---:|---:|---:|
| low | 60 | 21% | 22% | 20% | 95% | 85% |
| medium | 87 | 31% | 31% | **22%** | 93% | **62%** |
| high | 66 | 23% | 20% | **29%** | 86% | 77% |
| xhigh | 57 | 20% | 24% | 26% | 100% | 100% |
| max | 12 | 4% | 3% | 3% | 67% | 75% |

Router decision confusion (rows: label):

| label \ got | low | medium | high | xhigh | max |
|---|---:|---:|---:|---:|---:|
| low | 51 | 6 | 3 | 0 | 0 |
| medium | 3 | 54 | **27** | 3 | 0 |
| high | 2 | 3 | 51 | 10 | 0 |
| xhigh | 0 | 0 | 0 | 57 | 0 |
| max | 0 | 0 | 0 | 3 | 9 |

Subagent held-out: Jev top 100% exact, router decision 79%. Haiku recall
falls to 56%, and 7 of 33 decisions are above the label.

### Train (142 main cases × 3)

| | Jev top | router decision |
|---|---:|---:|
| exact | 85% | 80% |
| acceptable | 99% | 92% |
| mean abs rank error | 0.15 | 0.25 |

| tier | labels | label share | top share | decision share | recall (top) | recall (decision) |
|---|---:|---:|---:|---:|---:|---:|
| low | 105 | 25% | 29% | 26% | 100% | 89% |
| medium | 108 | 25% | 19% | 20% | 72% | 72% |
| high | 108 | 25% | 21% | 23% | 73% | 67% |
| xhigh | 87 | 20% | 28% | 28% | 100% | 94% |
| max | 18 | 4% | 3% | 4% | 67% | 83% |

Subagent train: Jev top 88%, decision 83%.

**Diagnosis.** On held-out cases Jev's own answer is right 92% of the time
and never skips medium. The policy between Jev and the decision loses 13
points: the router is the main bottleneck, not Jev. On train, which holds
the tuned originals and more long-context cases, Jev itself shows the
owner's pattern: low 29% and xhigh 28% of top answers for 25% and 20% of
labels. Its errors there are adjacent (medium → low 18, high → xhigh 26)
and inside the accept sets, so "acceptable" stays at 99%.

## 5. Where the policy loses: decision rule × warm gates

The comparison was run offline on the saved answers (base questions). Rows
cross the rule with which warm gates are active. "conf" keeps only the
confidence gate, "cont" keeps only the no-downgrade-while-continuing gate.
Loss is the mean realized cost against the label, in catalog cost units per
task, where working below the label costs k × the gap.

Held-out:

| rule | gates | exact | loss k=1 | loss k=2 | loss k=3 |
|---|---|---:|---:|---:|---:|
| argmax | both | 84.8% | 0.171 | 0.243 | 0.315 |
| cost κ=1.5 | both | 84.4% | 0.153 | 0.199 | 0.244 |
| cost κ=3 (baseline) | both | 78.7% | 0.194 | 0.209 | 0.224 |
| argmax | cont only | 87.2% | 0.135 | 0.208 | 0.281 |
| argmax | conf only | 89.0% | 0.139 | 0.211 | 0.283 |
| argmax | none | 92.6% | 0.098 | 0.171 | 0.244 |
| **cost κ=1.5** | **none** | **92.6%** | **0.080** | **0.121** | **0.161** |
| cost κ=2 | none | 89.4% | 0.104 | 0.144 | 0.185 |
| cost κ=3 | none | 85.1% | 0.143 | 0.153 | 0.163 |

Train:

| rule | gates | exact | loss k=1 | loss k=2 | loss k=3 |
|---|---|---:|---:|---:|---:|
| argmax | both | 81.7% | 0.253 | 0.375 | 0.498 |
| cost κ=3 (baseline) | both | 79.8% | 0.259 | 0.332 | 0.405 |
| argmax | none | 84.0% | 0.201 | 0.294 | 0.386 |
| **cost κ=1.5** | **none** | **84.3%** | 0.208 | 0.286 | 0.364 |
| cost κ=2 | none | 83.3% | 0.216 | 0.274 | 0.333 |
| cost κ=3 | none | 83.6% | 0.201 | 0.225 | 0.248 |

Two findings:

1. **The penalty κ=3 inflates.** With a free switch the cost-aware rule
   picks the tier of least expected loss. At κ=3, a 0.55/0.45 medium/high
   answer becomes high and a high/xhigh split becomes xhigh. Even
   bimodal answers go up: at κ=3, p(low)=0.6 with p(high)=0.4 gives high.
   κ=1.5 matches argmax's accuracy on both splits. On held-out it has the
   lowest realized loss at every k, even k=3, the penalty the old setting
   assumed. On train, κ=3 has a lower loss at k=3 for 0.7 points less
   accuracy. κ was never calibrated (the history said "to verify against
   the ledger").
2. **The warm gates freeze sessions.** The confidence gate kept the current
   tier whenever Jev's confidence was below 0.7. The continuation gate
   forbade downgrades when Jev said the prompt continues the work, and its
   criteria count "adding a fix to what is being built" as continuing. With
   per-turn effort both gates guard a switch that costs nothing. On warm
   cases only, the gates cost 22 points of held-out exactness (69% → 91%).
   They did not reduce under-provisioning: 5.6% of decisions below the
   label with the gates, 2.8% without. A session that went low stayed low
   on a new race (the confidence gate), and one that went xhigh stayed there
   for "fix the fixture too" (the continuation gate). That is the
   stickiness behind "low or xhigh".

Cutpoints on Jev's expected level (4 thresholds fitted on train) overfit:
train 90.6%, held-out 89.0%, below argmax.

## 6. Question and state variants

Each variant was run on all cases × 3 and scored with the same rule
(cost κ=1.5, no gates on free switches).

| variant | train exact | held-out exact | train recall low/med/high/xhigh | notes |
|---|---:|---:|---|---|
| **base** (current criteria and instruction) | 84.3% | 92.6% | 99/77/70/97 | kept |
| field paths in the instruction (`` `task` ``, `` `last_assistant` ``…, "a follow-up does not inherit…") | 84.3% | 90.1% | 94/75/74/100 | TypeSafe's "reference fields by path" |
| structured levels (`what`, `examples`, `not_for`) + paths | 83.6% | 94.7% | 96/79/77/87 | xhigh recall −10 on train |
| composite: level + 4 Scores (work, scope, unknowns, stakes), linear fit on train | 88.3% | 93.3% | 93/86/94/83 | weights: level 0.81, work 0.29, scope 0.91, others ≈ 0 |
| the 4 dimensions alone, fitted | 78.2% | 76.6% | 96/75/81/62 | under-provisions xhigh |
| no `current` tier in the state | 82.6% | 92.6% | 98/74/68/97 | see anchoring below |

None wins on both splits by more than the noise. The structured levels gain
2 points held-out but lose 0.7 on train and 10 points of xhigh recall. The
composite gains 4 points on the split it was fitted on and 0.7 held-out,
for 4 more questions. The current criteria stay. Of TypeSafe's guidance,
the relevant point is that each level is judged on its own: Jev never sees
level numbers or neighbours, so telling it that levels are ordered or
adjacent levels are close does nothing. The benchmark could not show a gain
from anchors or examples at this size, so re-test them when it grows.

**Anchoring on the current tier.** On the 81 non-initial cases, replacing
`current` with low or xhigh moves Jev's mean expected level from 0.98 to
1.12, about 0.14 rank, and up to 0.44 on single cases. It is real but
small, and dropping the field did not improve accuracy, so it stays.
Without the warm gates the anchor no longer compounds.

**Go-ahead after compaction.** Jev rates a bare "continue" after a
compaction as low (p=0.61 on a case whose pending work is an intermittent
502 hunt). The warm fast path already kept the tier for bare go-aheads, but
compaction and cold turns asked Jev. Conversely, a go-ahead that answers a
proposal ("There is no lock... Should I apply a fix?") starts new, bigger
work, and the fast path kept it on low.

## 7. Calibration and thresholds

- ECE of the top probability: 0.04–0.06 (main), so Jev's top probability
  is well calibrated against exact correctness. Subagent ECE is 0.20: Jev
  is under-confident there (mean top p 0.80, exact 100% held-out).
- Exact accuracy of Jev's top by confidence (train, main): < 0.5: 60%;
  0.5–0.6: 88%; 0.6–0.7: 46%; 0.7–0.8: 39%; 0.8–0.9: 83%; ≥ 0.9: 98%.
  Acceptable accuracy is 96–100% in every bucket. For a switch that costs a
  cache rebuild, `warm_min_confidence` 0.8 is where exact accuracy jumps
  (was 0.7).
- `continues_threshold` (train): yes-cases 0.78–0.95, no-cases spread up to
  0.94. There is no clean gap: 0.7 gives 24 false yes and 0 false no, 0.8
  gives 16 and 3, 0.85 gives 9 and 9. It is set to 0.8, and now only
  affects modes and costly switches.
- Ultracode threshold 0.75: unchanged. Train yes-cases stay above it, with
  6 false yes on 276 held-out answers.

## 8. Jev version

The Decisions API on OpenRouter answers only `typesafe/jev-1.13`
(`jev-1.13-20260917`). `jev-latest`, `jev-preview` and `jev-1.14` return
400, and TypeSafe's models page lists 1.13 as current. OpenRouter now lists
`typesafe/jev-router` (created 2026-09-25): it is a chat-completions router
that picks a model and effort for OpenRouter's own requests, not the
Decisions API. It is a possible future comparison, but not a Jev version to
shadow.

## 9. What changed

| where | change | why (numbers above) |
|---|---|---|
| `catalog.toml` | `underprovision_penalty` 3.0 → 1.5 | §5 |
| `catalog.toml` | `continues_threshold` 0.7 → 0.8 | §7 |
| `catalog.toml` | fast-mode reason and sources (stays off) | §10 |
| router | warm gates only when the switch costs something (cache rebuild) | §5 |
| config | `features.warm_min_confidence` 0.7 → 0.8 (costly switches) | §7 |
| hooks | a bare go-ahead after compaction or a pause keeps the tier (`Carry`); a go-ahead answering a proposal is routed | §6 |
| eval | metrics (§3), `--repeat`, `--split`, `--check`; mirrors the go-ahead rules | |
| skill | step 5/6 and `references/routing-eval.md` | |

### Final numbers

Held-out (94 main cases × 3):

| | baseline | final |
|---|---:|---:|
| router decision exact | 79% | **91%** |
| acceptable | 84% | 97% |
| mean abs rank error | 0.24 | 0.09 |
| decisions below / above the label | 11 / 49 | 12 / 12 |
| ECE (Jev top) | 0.04 | 0.05 |

| tier | label share | decision share (baseline → final) | recall (baseline → final) |
|---|---:|---:|---:|
| low | 21% | 20% → 22% | 85% → 95% |
| medium | 31% | 22% → 31% | 62% → 93% |
| high | 23% | 29% → 19% | 77% → 82% |
| xhigh | 20% | 26% → 24% | 100% → 100% |
| max | 4% | 3% → 3% | 75% → 75% |

Final router confusion (held-out):

| label \ got | low | medium | high | xhigh | max |
|---|---:|---:|---:|---:|---:|
| low | 57 | 3 | 0 | 0 | 0 |
| medium | 6 | 81 | 0 | 0 | 0 |
| high | 0 | 3 | 54 | 9 | 0 |
| xhigh | 0 | 0 | 0 | 57 | 0 |
| max | 0 | 0 | 0 | 3 | 9 |

Subagent held-out: decision exact 79% → 100%, haiku recall 56% → 100%.

Train (142 main cases × 3): decision exact 80% → 81%, acceptable 92% →
98%, MAE 0.25 → 0.19. Decision shares are low 29%, medium 18%, high 20%,
xhigh 30%, max 3%, against labels of 25/25/25/20/4. Recall is 100/67/64/100/67.
Subagent train: 83% → 88%.

**Regression gate** (`eval.DefaultGate`, main scope, held-out, 3 runs):
decision exact ≥ 88%, recall ≥ 80% for tiers with 20+ answers, each tier's
decision share within 6 points of its label share, and rank error ≤ 0.12.
The final router passes. The same code with the old penalty fails five
checks: exact 86%, MAE 0.14, medium 78%, high 77%, and xhigh at 27% of
decisions for 20% of labels.

Jev cost is unchanged (same questions): about $0.00004 per decision. A full
benchmark run (271 × 3) costs about $0.03.

## 10. Fast mode for low turns

Primary sources: platform.claude.com fast-mode and pricing pages, and
code.claude.com/docs/en/fast-mode and prompt-caching, read 2026-09-27.

- Price: Opus 5.5 fast costs $8 / $40 per MTok, against $4 / $20 standard,
  so 2× on every token. Prompt-caching multipliers apply on top.
- Speed: up to 2.5× output tokens per second. Time to first token does not
  improve. Tool execution and waiting are not sped up either.
- Cache: speed is part of the API's cache key ("requests at different
  speeds do not share cached prefixes"). Claude Code pays one uncached
  rewrite of the whole context at fast prices on the first fast turn of a
  conversation.
- Availability: research preview. Console organizations need access
  provisioned (429 otherwise), and subscriptions pay from usage credits
  only. Claude Code's availability check goes to `api.anthropic.com`
  directly, not through `ANTHROPIC_BASE_URL`, which is automodel's setup.

A low turn is the cheapest and shortest work: $0.55 and about 80 s per task
on the index. Fast mode would take it to about $1.10 and save at most about
48 s of generation, less in practice, plus a context rewrite at $8 (write
×1.25) per MTok the first time: $2 on a 200k context. It buys latency, not
quality, on the turns where latency matters least, and at a lower effort
the speed is already there. That fails the owner's rule (clearly worth it
for the work it gets), so `fast.allowed` stays false. Revisit if the
multiplier falls under about 1.3× or fast mode stops touching the cache key.

## 11. Limits and open questions

- **Labels are one reader's judgment**, and the cases are invented. The
  accept sets absorb honest ambiguity, but exact accuracy against a single
  label is only as good as the rubric. Grow the benchmark from real
  misroutes in organic sessions (text invented, never copied).
- **Jev's own low/xhigh lean on train is not fixed.** It shows on
  long-context follow-ups: medium → low on "ok fix it" / "apply your
  suggestions" after hard work, and high → xhigh on keyword-heavy
  feature work. Train decision recall is 67% for medium and 64% for high;
  the gate runs on held-out, which is easier. The composite and the
  structured levels helped train but did not hold on held-out. They are the
  first things to re-test with a larger benchmark or a new Jev.
- **κ=1.5 is fitted to labels, not to money.** The ledger should confirm
  it: warm switches up right after a switch down mean the penalty is too
  low.
- **The eval models free switches** (per-turn effort). Sessions without
  per-turn effort keep the gates, and those paths are only unit-tested.
- The subagent held-out split is small (11 cases), so its 100% is not
  strong evidence.
