# Effort levels against Anthropic's guidance (2026-10-14)

The owner's impression: automodel runs too high an effort overall. This note
compares the routing with Anthropic's model-selection and effort guidance,
measures where the effort really goes, and says what changed.

## 1. What Anthropic recommends

Read 2026-10-14:

- [Choosing the right model](https://platform.claude.com/docs/en/about-claude/models/choosing-a-model) (current)
- [Optimizing for cost and intelligence](https://platform.claude.com/docs/en/about-claude/models/optimizing-for-cost-and-intelligence)
- [Effort](https://platform.claude.com/docs/en/build-with-claude/effort)
- [Choosing the right Claude model](https://academy.claude.com/tutorials/choosing-the-right-claude-model) (Academy tutorial, claude.ai)
- [Claude Platform 101: choosing the right model](https://academy.claude.com/courses/claude-platform-101/choosing-the-right-model) (outdated: it still names Sonnet 5 and Haiku 4.5)

**Default and starting effort per model.**
- Opus 5.5 and Haiku 5.5 default to `medium`: "start there and adjust". "For most agent workloads, start with Claude Opus 5.5 at its default effort (`medium`)."
- Sonnet 5.5 defaults to `high`. "For agentic coding and multistep tool use, start with `medium` for well-specified tasks and move to `high` for harder or longer ones." `xhigh` and `max` "only where your evals show a quality gain".
- Haiku 5.5: `medium` for most work including agentic coding. `low` for chat and short tool tasks. `high` for knowledge work and longer agent tasks.

**What each level is for** (the effort page's table):

| Level | For |
|---|---|
| `max` | the deepest reasoning |
| `xhigh` | "long-running agentic and coding tasks (over 30 minutes) with token budgets in the millions" |
| `high` | "complex reasoning, difficult coding problems, agentic tasks" |
| `medium` | "agentic tasks that require a balance of speed, cost, and performance" |
| `low` | "simpler tasks that need the best speed and lowest costs, such as subagents" |

**Measured trade-offs** (cost-and-intelligence page, Anthropic-internal):
- `medium` "matched the default's accuracy at about 70% to 87% of its cost".
- On SWE-bench Pro, Opus 5.5:
  - `medium` is about 2.5 points below `high`, for about 70% of the cost;
  - `xhigh` is about 1.4 points above `high`, for 2.5 times the cost: "a real tradeoff".
- "Hard work does not automatically need high effort." "Sweep two or three effort levels on a sample of your own traffic."
- Haiku 5.5 answered GPQA Diamond at about a twentieth of Opus 5.5's cost, 85% against 91%: "high-volume and latency-sensitive work with checkable outputs".

**Model positioning.**
- Opus 5.5: complex agentic coding and enterprise work.
- Sonnet 5.5: speed and capability for everyday coding and agents. The Academy tutorial calls it "your versatile default".
- Haiku 5.5: lowest latency and price, including sub-agent tasks.
- The outdated course says "the right model is the cheapest one whose output you'd actually ship".

## 2. Where automodel stands

Home ledger, 2026-10-07 to 2026-10-14 unless noted.

### Main thread (12% of spend since 10-11: $554 of $4,416)

The 737 main decisions break down as follows:

| Level | Decisions | Jev's reading of the prompt itself |
|---|---|---|
| high | 50% | 26% |
| xhigh | 27% | 7% |
| medium | 12% | 19% |
| low | 11% | 49% |

- **The high share comes from holds.** 412 decisions (56%) hold the work's level: follow-ups, go-aheads, mid-turn prompts, peer messages. The work levels are high 58% and xhigh 30%. Holding is deliberate, because lowering follow-ups drew the owner's complaints on 2026-10-06 and 2026-10-08.
- **Our ladder sits one notch above Anthropic's table:**
  - our medium is "a routine change with a clear recipe", where Anthropic's low is "simpler tasks";
  - our high is a multi-file change with design choices, close to Anthropic's medium ("agentic tasks");
  - our xhigh is "a hard problem where a wrong answer is expensive", which Anthropic's table puts at high ("complex reasoning, difficult coding problems"); its xhigh is for 30-minute-plus runs.
  - Our eval labels follow our ladder: main train xhigh 33%, test 36%.
- **The money at stake is small.** Effort changes output and the number of turns; cache reads dominate. Per-task owner cost is 0.80 at medium, 1.00 at high and 1.61 at xhigh (model-mix note).
  - Moving every xhigh work to high would save about $180 a week.
  - Moving every high work to medium would save about $105 a week.
  - Together that is under 3% of the week's $9.5k list-price-equivalent spend.
- **Not changed.** Re-mapping the main ladder means relabelling most of the eval set, and it is a quality trade-off the owner pushed back on twice. The numbers say it is not where the spend is.

### Subagents (88% of spend)

Routed shares since 10-11: sonnet-xhigh 66%, sonnet-high 14%, opus-xhigh 12%.

**Finding: the routed effort of a subagent is not applied.**
- Claude Code (2.1.29x) puts `output_config.effort: "medium"` on the first system message of every subagent conversation, the deferred-tools notice. The API applies that statement over the top-level effort automodel sets.
- Measured in the dev harness:
  - Claude Code sends `medium` whatever the agent was routed to, and also with `effortLevel: xhigh` in the session's settings.
  - With the statement stripped, the same agent at a routed xhigh produced more output: 210 vs 170, 268 vs 146, and 1,518 vs 885 tokens on a hard counting puzzle (3 of 3 pairs).
- In the ledger, output per request is flat across routed agent efforts:

  | Model | high | xhigh | max |
  |---|---|---|---|
  | Sonnet 5.5 | 994 | 943 | — |
  | Opus 5.5 | 517 | 937 | 879 |

  - Routed main efforts do differ, because the main thread has no such statement: automodel's per-turn marks are the only ones there.
- **So subagents already run at `medium`.** That is Anthropic's recommended start for well-specified agentic work on Sonnet 5.5. For agents, only the model part of a decision is real.
- **Side effects:**
  - The cost estimates per agent effort (e.g. Sonnet high 0.33 vs xhigh 0.62) don't describe what runs.
  - "Overkill" on agents is a model question (Opus vs Sonnet), not an effort question.

**Effort changes don't rewrite an agent's cache.**
- Real ledger since v0.27.1: 52 of the 54 requests right after a same-model effort change read 95% or more of the context from cache.
- The two exceptions: one after more than an hour idle (cache expired), one at 68% read.
- The hook prices such a change as a full rewrite (`agentSwitchCost`). That is wrong, but it doesn't matter while the effort is overridden.

## 3. What changed

- **Ledger:** usage rows record `turn_effort`, the effort of the request's last per-message statement, i.e. the one the API applies. A few days of it will say what effort agents run at in the owner's real sessions, not only in the harness.
- **Refresh skill** (`.claude/skills/refresh-model-catalog`): Anthropic's model-selection and effort pages are now a required source (step 2), and step 5 checks the decision and label mix against them. A deviation is either changed or justified with ledger data. The Claude Code subagent-effort fact is in `references/sources.md`.
- **No change to criteria, labels, penalty or default tiers.** `default_main_tier = "medium"` would follow Opus 5.5's default, but warm eval cases without a current tier take the default tier as current, so the change needs a full eval run. It is worth a few fallback decisions only.

## 4. Open decision

Should routed agent effort be enforced? That means stripping Claude Code's statement from routed agents' requests, so automodel's effort applies.

- **As things stand, it would raise spend.** 66% of agents are routed to sonnet-xhigh, about twice Sonnet medium's per-task cost on the owner's traffic, and they run at medium today.
- **It only pays together with a guidance-aligned subagent ladder:**
  - Sonnet medium for well-specified work;
  - Sonnet high for harder or longer work;
  - xhigh and Opus where the eval shows the gain.
- **Measure first.** Use `turn_effort` on real traffic, and check whether the medium agents' work is good enough, before deciding. The owner has not complained about agent quality while they ran at medium.

## 5. Follow-up (2026-10-14, v0.29.0): the agent ladder follows the guidance, and its effort applies

The owner asked to align the subagent ladder with Anthropic's guidance and
to make the routed effort real.

**New ladder** (cost per task on the primary benchmark, all on its frontier):

| Tier | Model, effort | For | Cost |
|---|---|---|---|
| haiku | Haiku 5.5 medium | mechanical, fully specified work | 0.14 |
| sonnet-medium | Sonnet 5.5 medium | well-specified work (the default) | 0.48 |
| sonnet-high | Sonnet 5.5 high | harder or longer work: design, prototypes, reviews of a diff, bugs of unknown cause | 0.88 |
| opus-high | Opus 5.5 high | complex reasoning: intermittent bugs, reviews of concurrent/security/money code, proofs, lasting architecture | 1.82 |
| opus-xhigh | Opus 5.5 xhigh | long autonomous work, well over 30 minutes | 3.46 |
| opus-max | Opus 5.5 max | the deepest reasoning | 5.98 |

Dropped: sonnet-low (Haiku medium and Sonnet medium cover it) and
sonnet-xhigh (dominated by Opus high on the primary benchmark; Anthropic keeps
xhigh for 30-minute-plus runs and Sonnet xhigh "only where evals show a gain").

**The effort now applies** (`features.enforce_agent_effort`). The proxy
restates the effort of Claude Code's own statements in a routed agent's
conversation and adds per-turn statements, as on the main thread: a later
change (a message sent to the agent) keeps its cache. In the ledger since
v0.28.0 the applied effort was mixed, not always medium: teammates often ran
the parent's xhigh, some agents had no statement (the routed top-level effort
applied), others ran Claude Code's medium. Verified in the dev harness:
- turn_effort = the routed effort (high) with the switch on, medium off (4 pairs);
- a 5-request agent reads its cache on every request (writes 642, 78, 78, 78 tokens after the first).
Agents on a retired tier keep what Claude Code states.

**Relabelled eval cases (subagent scope).** Each old label class maps to the
tier the guidance gives that work:
- sonnet-low and sonnet-high → sonnet-medium (well-specified work: Sonnet medium);
- sonnet-xhigh → sonnet-high (harder or longer: Sonnet high);
- opus-xhigh → opus-high (complex reasoning, difficult code: high; xhigh is for long runs);
- opus-max stays (accepting opus-xhigh);
- two explicit edits (`sh-callsite-arg`, `sh-flaky-skip`) → haiku (Haiku medium covers agentic coding of fully specified edits);
- five new invented train cases: four long autonomous runs at opus-xhigh, one short review at sonnet-medium.

**Measured** (3 repeats; subagent cases only; heldout4/5 have none):

| Split | exact | acceptable | rank error | per-tier recall |
|---|---|---|---|---|
| train, before (old ladder, old labels) | 81% | 99% | 0.21 | sonnet-low 71%, sonnet-high 72%, opus-max 0% |
| train, after | 90% | 100% | 0.12 | ≥ 88% except opus-max (1 case, 0%) |
| test, before | 90% | 100% | 0.10 | haiku 67% |
| test, after (run once) | 85% | 100% | 0.15 | haiku 67%, sonnet-high 67% (two design stages read opus-high) |

On the eval cases the old decisions map to the new ones almost one to one:
sonnet-xhigh → sonnet-high 94% (opus-high 6%), opus-xhigh → opus-high 100%,
sonnet-high → sonnet-medium 94%.

**Weekly cost** (Home, 2026-10-07 to 10-14, $7,439 of subagent traffic at list
prices; owner-traffic cost per effort from the model-mix note, Opus high = 1:
Sonnet medium 0.20, high 0.33, xhigh 0.62; Opus high 1.00, xhigh 1.61, max 2.90):
- if Sonnet agents ran at medium and Opus agents at their routed effort (what the
  ledger's output per request suggests): $7,439 → $8,219 (+10%): Sonnet agents
  routed to the harder level now really run at high (+$1.5k), Opus xhigh
  agents move to high (−$0.7k);
- with the mix measured since v0.28.0: −11%; if every agent ran at medium: +53%.
The guidance, not the bill, sets the ladder: the likely outcome costs about 10% more.

**Audit: does every override take effect?** (Home ledger, week to 2026-10-14; Jev costs about $0.0003 a call, so the calls themselves cost cents a week)

| Path | Jev calls/week | Effect verified | Action |
|---|---|---|---|
| Agent spawn (Agent tool) | 160 | model: yes (alias); effort: mostly no (Claude Code's statement won) | effort made effective (enforce_agent_effort, harness turn_effort = routed) |
| Resumed agent (SendMessage) | 1,484 | model changes 102: yes since v0.27.1; effort-only changes 105: mostly no | made effective, and priced as free (per-turn statements keep the cache) |
| Fork | 0 (forks_inherit) | model: parent's; effort: no statement in forks, the top-level (main's) applies | kept. 10 real forks wrote 170-350K of cache; not automodel's marks (harness: a fork reads the parent's cache with or without them), a parent-marks change had no effect and was dropped |
| Named-model agent | 0 when respected | — | kept |
| Workflow stage | 3 | model: yes (injected); effort: the stage's option becomes Claude Code's statement (dump) | kept |
| Main, peer message | 80 | 34 of 181 since 09-30 changed the level, the work or the mode | kept |
| Main, mid-turn | 81 | 4 changed this week | kept |
| Main, scheduled | 67 | own level for status checks (since v0.22) | kept |
| Main, compaction | ~45 | 7 new works and 1 mode of 83 since 09-30 | kept |
