# Where the usage goes, and whether subagents get the right model (2026-10-06)

Trigger: the owner's question "we are almost always on Opus high/xhigh;
shouldn't more of it go to Sonnet 5.5?". The goal was good choices, not
just cheaper ones. Data: the owner's ledger over 7 days (2026-09-29 to
2026-10-06), the transcripts and workflow scripts, and fresh sources on
Sonnet 5.5. Real prompts are not reproduced here.

## 1. Where the usage goes

API-equivalent spend over the 7 days was $8.1k at list prices. Cache reads
cost the same $0.20/M on Sonnet 5.5 and Opus 5.5.

| Share | What |
|---|---|
| 44% | Subagents and workflow agents of sessions that had left the custom model |
| 9% | Main threads of those sessions |
| 24% | Routed subagents of routed sessions |
| 10% | Routed main threads |
| 9% | Unrouted subagents of routed sessions (scripts naming a model) |

**53% ran outside routing.** Claude Code's `--resume` restores the model
of the session's last answer, and the proxy answered with the model it
served (`claude-opus-5-5`). After a reboot (2026-10-04) and a terminal
restart (2026-10-02) jaunt resumed every session, and each came back on
plain Opus: the main thread at the owner's default effort (xhigh), every
subagent and workflow agent on Opus. Fixed in 0.23.0: a routed response
names the custom model. Checked with real Claude Code 2.1.291 in an
isolated config.

Main threads: cache reads are 53–56% of their cost, cache writes 33–38%,
output about 10%. Sonnet 5.5 halves writes and output, not reads: at most
about 20% less at equal tokens, less once its extra steps count. A model
switch also rewrites the whole cache. Context size weighs more than the
model: a 650k-token context costs about $0.13 per request in reads alone.

## 2. Sonnet 5.5 since 2026-09-29

Little changed. AA re-ran three of its evals: index ±0.1, tokens per task
+1–8%. vals.ai's Terminal-Bench 4.0 at max: Sonnet 64.1% (was 53.0%), Opus
65.2% (was 61.6%). CursorBench, FrontierCode and prices: unchanged. The
Coding Agent Index still uses the pre-release Sonnet build, and Opus 5.5
appears at max only.

At the top efforts, Sonnet costs more per task than Opus:

| Source (effort) | Sonnet 5.5 | Opus 5.5 |
|---|---|---|
| Coding Agent Index (max) | $14.19, 266 steps | $13.04, 155 steps |
| AA Terminal-Bench 4.0 (max) | $18.76 | $13.11 |
| AA index at about the same score (56) | max, $7.67 | xhigh, $3.46 |

Subscription limits: no official source gives an Opus-to-Sonnet weight.
Max has a weekly limit shared by all models, plus per-family limits
("You've hit your Opus limit", "…Sonnet limit"). Since 0.23.0, each
ledger usage line carries `limits` (5h and 7d utilization, 0–1, from the
`anthropic-ratelimit-unified-*` headers), to measure the real weight on
this account.

No catalog change: the 2026-09-29 decision stands (Sonnet low and high
for subagents, no main tier).

## 3. Are subagents routed well?

77 subagent decisions in the 7 days: 23 Agent calls and 54 workflow
stages. Each was reviewed against its actual task.

- **Agent calls: sound.** They are mostly multi-file features,
  investigations and reviews, at opus-high or opus-xhigh. Mechanical work
  went to Haiku, documentation lookups to Sonnet.
- **Workflow stages written out in full: sound.** Implementers ran at
  high, adversarial verifiers at xhigh, judges at high.
- **Blind workflow stages: wrong.** The prompt is built from the script's
  data (`BASE + '\n\n' + d.p`, `${CTX}\n\n${s.angle}`, `t.prompt`) and
  carries almost no text. Jev read them at confidence near 0. **Every
  Sonnet or Haiku pick in the owner's workflows came from such a stage.**
  They sent adversarial security reviews to Sonnet low, and migration
  plans, designs and dozens of module implementations to Sonnet.

The suspicion was that too much ran on Opus. What the data shows is the
opposite: the heavy work wrongly put on Sonnet came from stages the
router could not read.

### Fix (0.23.1)

A stage prompt with under 100 characters of literal text that reads names
from its script now shows Jev what those names hold:

- the array a loop parameter ranges over, one agent per element, found
  through `.map`, `pipeline` or a derived `.filter`;
- the constants and prompt builders it uses, with shared preambles cut to
  500 characters.

A stage whose data can't be found keeps the session's model and effort.

Measured on the 56 blind stages of the owner's real workflows, labelled
from the agents' actual prompts, 3 runs:

| | Acceptable | Under | Over |
|---|---|---|---|
| Before | 43% | 57% | 0% |
| After | 82% | 0% | 18% |

The over-provisioned picks are mostly one rank (xhigh where high
suffices); 9 of 168 reached max, under project preambles that stress
the stakes.

Anonymised eval, 8 new blind-stage cases (the state the hook now sends),
3 runs:

| Split | Before | After |
|---|---|---|
| train | 5/12 | 12/12 |
| test (written before measuring) | 3/12 | 12/12 |

The subagent eval set (67 cases, 3 runs) still shows the router's
decision at 83% exact and 99% acceptable. The over-provisioning on the
real blind stages comes from how Jev reads large project context, not
from the subagent policy, so the policy stays.

## Open questions

- Opus vs Sonnet weight on the owner's quota: read the `limits` deltas in
  the ledger after a few days of use.
- Blind stages read at max under high-stakes preambles: watch the ledger.
  If it persists, shorten the shared context further, or cap stages whose
  task is reading or mapping.
