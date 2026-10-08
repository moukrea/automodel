# The model mix, priced on the owner's traffic (2026-10-08)

Trigger: the owner's review. Routing sent almost all work to Opus 5.5, and
the question was whether Haiku 5.5, Sonnet 5.5 and Fable 5.1 should take
some of it. The answer had to account for prices, the work each model does
per task, and the cache a model switch destroys.

Everything below was read or computed on 2026-10-08. Real prompts are not
reproduced here.

## 0. Summary

**Proposed mix.**

| Scope | Level (tier) | Before | After | Why |
|---|---|---|---|---|
| main | asked Haiku tier | Haiku 4.5, no thinking | **Haiku 5.5 low**, `max_context` 100K | Haiku 4.5 is only guaranteed until 2026-10-15. Haiku 5.5 is a tenth of its price up to 100K prompt tokens and scores higher. |
| main | low → max | Opus 5.5 low → max | unchanged | A Sonnet main tier never pays back its switch costs (section 6). |
| subagent | haiku | Haiku 5.5 medium | unchanged | |
| subagent | sonnet-low | Sonnet 5.5 low | unchanged | |
| subagent | opus-low | Opus 5.5 low | **merged into sonnet-high** | Sonnet high scores higher on every per-effort coding benchmark, at 0.70x the owner cost. |
| subagent | sonnet-high | Sonnet 5.5 high | Sonnet 5.5 high | Its criteria also take opus-low's small changes and checks. |
| subagent | opus-medium + opus-high | Opus 5.5 medium, high | **sonnet-xhigh** (Sonnet 5.5 xhigh) | 0.62x of Opus high's owner cost (0.83x pessimistic). At least Opus medium's score on 4 of 5 benchmarks; 1.7–2.9 points under Opus high's on 4. |
| subagent | opus-xhigh | Opus 5.5 xhigh | unchanged | Opus clearly wins here: Sonnet xhigh is 2.5–4.9 points lower on 4 of 5. |
| subagent | opus-max | Opus 5.5 max | unchanged | |
| — | Fable 5.1 | out | out | No frontier position on any coding benchmark, at list price or on the owner's traffic. |

**Money.** The owner's last 7 days were replayed at today's prices with a
1-hour cache TTL, as v0.24.0 now runs subagents.

| | Before | After (central) | After (pessimistic) |
|---|---:|---:|---:|
| Routed subagents (the ones a subagent tier decides) | $3,745 | $3,133 (−16%) | $3,509 (−6%) |
| Same, without the one 5-day opus-max agent | $2,700 | $2,089 (−23%) | $2,465 (−9%) |
| All subagents (routed + $2,210 the tiers don't decide) | $5,954 | $5,343 (−10%) | $5,719 (−4%) |
| Main sessions | $1,791 | unchanged | unchanged |

The pessimistic case assumes Sonnet takes 1.37x the relative steps
CursorBench shows (section 4). The 1-hour subagent TTL (v0.24.0) saves
more than the mix: as billed with the 5-minute TTL, the same subagent work
cost $7,445. The forward cost is $5,954, −20%.

**Quality.**

- At the opus-high level (41% of routed spend), Sonnet xhigh is 1.7–2.9
  points under Opus high on four per-effort coding benchmarks and +0.5 on
  AA's Terminal-Bench.
- Weighted over all routed spend, that is −0.6 to −1.1 points on four
  benchmarks and +0.4 on one.
- Sonnet xhigh is also 1.4–1.5x slower per task.
- The two other changes (opus-low, opus-medium) score as well or better than
  before.

**Routing quality (eval, train split, 3 runs).**

- **Main scope:** unchanged. Decision exact is 92.6% before and 92.7% after.
- **Subagents:** decision acceptable 100% → 100%. Decision exact 85.4% →
  84.6% on the same 41 cases, which is one answer of 123.
- **Test split (run once):** main 86.2% → 86.6%; subagents 87.2% → 88.5%.
- **Regression gate:** it fails on both catalogs, for failures the current
  catalog already has (section 8).

## 1. Sources

All were read on 2026-10-08. Raw copies and the parsing scripts are in the
session scratchpad (`models4-research.md`, `m4/`).

- **Anthropic**
  - Pricing: https://platform.claude.com/docs/en/about-claude/pricing. The model table, the prose and the Sonnet 5.5 model page all agree on the $0.10 Sonnet cache read since 2026-10-07.
  - Model pages: https://platform.claude.com/docs/en/models/{haiku-5-5,sonnet-5-5,opus-5-5,fable-5-1}/overview
  - Effort parameter: https://platform.claude.com/docs/en/build-with-claude/effort
  - Announcements, with chart data in the HTML: https://www.anthropic.com/claude-haiku-5-5 (2026-10-07), https://www.anthropic.com/claude-sonnet-5-5 (2026-09-28), https://www.anthropic.com/claude-opus-5-5 (2026-09-22), https://www.anthropic.com/claude-fable-and-mythos-5-1 (2026-09-01)
  - Claude Haiku 5.5 system card (2026-10-07)
- **Artificial Analysis**
  - Model pages, one per effort variant: https://artificialanalysis.ai/models/claude-{haiku-5-5,sonnet-5-5,opus-5-5,fable-5-1}. These carry Intelligence Index v4.3.2 and per-eval fields. Sonnet is repriced at $0.10. Haiku is priced at the ≤100K rate on every token, by AA's own note.
  - Coding Agent Index v1.5: https://artificialanalysis.ai/agents/coding-agents (materialized 2026-10-08T07:42Z). It runs Claude Code. The Haiku and Sonnet rows come from pre-release endpoints. Opus and Fable are at max only.
- **Others**
  - CursorBench 4.0: https://cursor.com/cursorbench (changelog 2026-10-07: Sonnet repriced). It gives steps and tokens per task for every config.
  - FrontierCode 1.1: https://cognition.com/frontiercode and the announcements' chart data. It runs Claude Code. Haiku scores are read off the system card's figure, and no Haiku cost is published.
  - Claude Code 2.1.294 embedded model catalog (`strings`, nothing executed). The `haiku_55` pricing has a `long_prompt` tier above 1e5 tokens. Sonnet 5.5 is still at `cache_read: 0.2`.
- **The router ledger** (`~/.local/state/automodel/ledger.jsonl`, read only), 2026-10-01 10:07 to 2026-10-08 10:07 (+02:00). Workflow journals (`~/.claude/projects/*/*/subagents/workflows/*/journal.jsonl`) were used to tie workflow agents to their stage decisions.

## 2. Prices ($/MTok)

| Model | Input | Output | Write 5m | Write 1h | Read |
|---|---:|---:|---:|---:|---:|
| Fable 5.1 | 10 | 50 | 12.50 | 20 | 0.25 |
| Opus 5.5 | 4 | 20 | 5 | 8 | 0.20 |
| Sonnet 5.5 | 2 | 10 | 2.50 | 4 | **0.10** (was 0.20 until 2026-10-07) |
| Haiku 5.5, prompt ≤ 100K | 0.10 | 0.50 | 0.125 | 0.20 | 0.01 |
| Haiku 5.5, prompt > 100K (whole request) | 0.50 | 2.50 | 0.625 | 1.00 | 0.05 |

Sonnet 5.5 is now exactly half of Opus 5.5 on every price. Above 100K,
Haiku 5.5 is a quarter of Sonnet on input, output and writes, and half on
reads.

## 3. Where the money goes (ledger, 7 days)

**Main sessions.**

- Cost: $1,791 at the 1-hour write price (7,995 requests), of which $1,216 is the 17 routed main sessions (5,457 requests).
- Context: median 556K tokens, p90 861K.
- Split: cache writes 46%, cache reads 46%, output 7%.
- How long a tier holds (median run of consecutive requests): low 11, medium 10, high 17, xhigh 27.5.

**Subagents.**

- Agents: 421 on Opus or Sonnet (plus 5 on Haiku 4.5), 51,189 requests.
- Cost: $7,445 as billed (5-minute writes); $9,535 at 1-hour write prices.
- Size: an agent's median is 58 requests, starting at 32K tokens and peaking at 206K. The median request is 279K tokens.
- **88.6% of requests and 96.2% of the cost are past Haiku 5.5's 100K price step.**
- **Cache rewrites:** 606M of the 704M tokens written were rewrites, 467M of them after a 5–60 minute pause (5-minute TTL expiry).
  - With the 1-hour TTL (v0.24.0) those rewrites become reads.
  - The same work then costs $5,954, the forward baseline used below.

**Which subagents the tiers decide.** Workflow agents show no tier in the
ledger, because the script carries their model and effort. They were matched
to their stage decision through the workflow journals.

| Source | Agents | Forward cost |
|---|---:|---:|
| Agent tool, tier recorded | 53 | $2,739 |
| Workflow stages, matched to their decision | 249 | $1,006 |
| **Routed by a subagent tier** | **302** | **$3,745** |
| Inherit the main decision (forks, stages without a readable prompt) | 62 | $894 |
| Not routed (a model named by the caller, unmatched stages, long-lived agents) | 57 | $1,316 |

Forks must stay on the main model to read its cache, so the inherited
agents are not a lever.

**One agent cost $1,044 forward ($1,267 as billed):** 6,232 requests over
five days, at a mean context of 508K tokens. It ran at opus-max.
- Jev's reading was split: opus-high 0.43, opus-max 0.42, opus-xhigh 0.15.
- The cost-aware rule (penalty 1.5) then picks max.
- That is a policy matter, not a model-mix one (section 9).

## 4. Method: replaying the owner's requests under another config

`scripts/replay.py` in the skill (stdlib, read-only) prices each agent's real
requests again as if another (model, effort) had done the same work. All
multipliers are relative to the config the agent actually ran.

**Inputs.**

- **Steps and output tokens per task: CursorBench 4.0**, the only source
  with every model at every effort.
  - Steps:
    - Opus 28/54/68/109/185
    - Sonnet 18/22/41/78/170
    - Haiku 25/39/59/93/163
    - Fable 51/63/77/101/128
  - Tokens:
    - Opus 15.8K/38.0K/53.1K/101.1K/218.4K
    - Sonnet 11.7K/16.0K/37.4K/100.2K/271.9K
    - Haiku 23.4K/42.7K/77.1K/143.8K/325.9K
    - Fable 34.8K/45.4K/58.4K/87.3K/117.2K
- **Cross-checks.**
  - FrontierCode's output tokens (Claude Code) give the same Sonnet/Opus
    ratios below max: low 0.70 vs 0.74, medium 0.41 vs 0.42, high 0.50 vs
    0.70, xhigh 0.79 vs 0.99.
  - The Coding Agent Index (Claude Code) gives the same Haiku/Sonnet step
    ratios: Haiku medium vs Sonnet low 2.37 vs 2.17; Haiku high vs Sonnet
    high 1.59 vs 1.44.
  - **At max they disagree:** in Claude Code, Sonnet takes 1.72x Opus's
    steps; CursorBench says 0.92x. The pessimistic basis scales Sonnet's
    steps and tokens by 1.37 (the geometric middle) against Opus.

**How a replay works.**

- **Context growth.** More steps carry more tool output. Total input grows
  as steps^1.3, fitted on the Coding Agent Index's Haiku and Sonnet rows.
  At equal steps, input per step is the same for both models. So the
  replayed agent takes `s·n` steps, and its context grows `s^0.3` times as
  much.
- **Writes and reads.** Each request writes what the context grew by and
  reads the rest.
- **Haiku's price step.** Haiku pays its higher rate on every request whose
  replayed prompt passes 100K.
- **Rewrites.** Rewrites from the real run are wall-clock events, so they
  keep their count on any model and scale with the context. With the
  1-hour TTL, only those after a pause under 5 minutes or over an hour are
  kept.
- **Validation.** With no multiplier and every rewrite kept, the engine
  gives $9,597 against the ledger's $9,535 at the same prices (+0.7%).

**Main sessions.**

- Each run of consecutive requests at a tier ("stretch") can move to
  another model.
- **The cost of moving:**
  - The first request on the new model writes the whole context there.
  - Coming back, Opus still has its own prefix cached if the stretch lasted
    under an hour; it writes what the stretch added (at $8/M).
  - Otherwise it writes the whole context.
- Two policies are priced:
  - **Every stretch:** switch on every stretch at that tier.
  - **Oracle:** switch only on the stretches that pay back. No router can
    know this in advance, so it is an upper bound on savings.

## 5. Frontiers per benchmark: list price vs the owner's traffic

**Owner cost.** The cost of all 302 routed agents' work if every agent had
run that config, with Opus high = 1.

| Config | low | medium | high | xhigh | max |
|---|---:|---:|---:|---:|---:|
| Haiku 5.5 | 0.07 | 0.10 | 0.16 | 0.27 | 0.54 |
| Sonnet 5.5 (central) | 0.18 | 0.20 | 0.33 | 0.62 | 1.43 |
| Sonnet 5.5 (pessimistic) | 0.22 | 0.26 | 0.43 | 0.83 | 1.97 |
| Opus 5.5 | 0.47 | 0.80 | 1.00 | 1.61 | 2.90 |
| Fable 5.1 | 1.46 | 1.72 | 2.03 | 2.62 | 3.28 |

**Frontier matrix.** Each cell gives the score, then two marks: the first is
the frontier on the owner cost (central), the second the frontier on the
benchmark's own list-price cost per task.

- ✓ means on the frontier, ✗ dominated, · no published cost.
- The benchmarks:
  - **TB 4.0 Anthropic:** Terminal-Bench 4.0 as Anthropic ran it (Claude Code `--bare`). Sonnet is costed at $0.10; Opus comes from the older pages.
  - **FrontierCode:** Claude Code runs. No Haiku cost; Sonnet is costed at $0.20.
  - **Coding Agent Index:** Claude Code. The Haiku and Sonnet rows are pre-release builds; Opus and Fable are at max only.
  - **CursorBench:** Cursor's harness.
  - **AA TB 4.0** and **AA index:** Haiku's list costs are lower bounds there.

| Config | owner cost (central / pess.) | TB 4.0 Anthropic | FrontierCode | Coding Agent Index | CursorBench | AA TB 4.0 | AA index |
|---|---:|---|---|---|---|---|---|
| Haiku low | 0.07 / 0.07 | 12.7 ✓✓ | 34.7 ✓· | 28.2 ✓✓ | 30.9 ✓✓ | 12.6 ✓✓ | 29.45 ✓✓ |
| Haiku medium | 0.10 / 0.10 | 20.3 ✓✗ | 41.6 ✓· | 33.6 ✓✓ | 36.9 ✓✓ | 15.2 ✓✓ | 34.46 ✓✓ |
| Haiku high | 0.16 / 0.16 | 24.8 ✓✗ | 41.9 ✓· | 35.1 ✓✓ | 42.3 ✓✓ | 21.7 ✓✓ | 37.82 ✓✓ |
| Haiku xhigh | 0.27 / 0.27 | 31.5 ✓✗ | 45.8 ✓· | 41.4 ✗✗ | 44.3 ✓✓ | 29.3 ✗✓ | 41.25 ✓✓ |
| Haiku max | 0.54 / 0.53 | 39.2 ✗✗ | 46.4 ✗· | 36.5 ✗✗ | 48.4 ✓✓ | 32.8 ✗✓ | 43.4 ✗✓ |
| Sonnet low | 0.18 / 0.22 | 20 ✗✓ | 29.3 ✗✓ | 42.1 ✓✓ | 35.8 ✗✗ | 20.7 ✗✗ | 35.87 ✗✗ |
| Sonnet medium | 0.20 / 0.26 | 28.8 ✓✓ | 36.5 ✗✓ | 45.9 ✓✓ | 39.2 ✗✗ | 29.8 ✓✗ | 40.84 ✓✗ |
| Sonnet high | 0.33 / 0.43 | 43 ✓✓ | 49.4 ✓✓ | 55 ✓✓ | 47.8 ✓✗ | 43.9 ✓✓ | 46.75 ✓✓ |
| Sonnet xhigh | 0.62 / 0.83 | 61.5 ✓✗ | 52.1 ✓✗ | 62.9 ✓✓ | 53.1 ✓✓ | 57.1 ✓✓ | 51.9 ✓✗ |
| Sonnet max | 1.43 / 1.97 | 70.6 ✓✓ | 46.2 ✗✗ | 68.4 ✓✓ | 55.5 ✗✗ | 63.6 ✓✓ | 56 ✓✓ |
| Opus low | 0.47 / 0.47 | 38.5 ✗✓ | 47.3 ✗✓ | – | 43.7 ✗✗ | 31.3 ✗✗ | 42.31 ✗✗ |
| Opus medium | 0.80 / 0.80 | 57.6 ✗✓ | 54.64 ✓✓ | – | 52.5 ✗✗ | 52.5 ✗✓ | 51.24 ✗✓ |
| Opus high | 1.00 / 1.00 | 64.2 ✓✓ | 53.99 ✗✗ | – | 56 ✓✓ | 56.6 ✗✓ | 53.58 ✓✓ |
| Opus xhigh | 1.61 / 1.60 | 66.4 ✗✓ | 51.42 ✗✗ | – | 56 ✗✗ | 59.6 ✗✓ | 55.99 ✗✓ |
| Opus max | 2.90 / 2.87 | 64.8 ✗✗ | 54.43 ✗✗ | 66 ✗✗ | 57.8 ✓✓ | 59.6 ✗✗ | 57.62 ✓✓ |
| Fable low | 1.46 / 1.48 | 40.2 ✗✗ | 52.8 ✗✗ | – | 45.1 ✗✗ | 40.4 ✗✗ | 46.82 ✗✗ |
| Fable medium | 1.72 / 1.73 | 43.4 ✗✗ | 50.91 ✗✗ | – | 46.8 ✗✗ | 44.9 ✗✗ | 48.92 ✗✗ |
| Fable high | 2.03 / 2.04 | 49.4 ✗✗ | 50.34 ✗✗ | – | 49.2 ✗✗ | 52 ✗✗ | 51.15 ✗✗ |
| Fable xhigh | 2.62 / 2.62 | 51.3 ✗✗ | 48.73 ✗✗ | – | 51.6 ✗✗ | 55.1 ✗✗ | 53.2 ✗✗ |
| Fable max | 3.28 / 3.26 | 55.8 ✗✗ | 50.28 ✗✗ | 62.2 ✗✗ | 51.8 ✗✗ | 52 ✗✗ | 53.35 ✗✗ |

**What the owner's traffic changes, compared with list prices.**

1. **Sonnet gets cheaper.** Cache reads dominate, and Sonnet's are half of
   Opus's.
   - Sonnet xhigh drops from about Opus high's price at list (index $2.01
     vs $1.82; Anthropic's Terminal-Bench $4.34 vs $3.88) to 0.62.
   - Sonnet high and xhigh are on the owner frontier on every benchmark.
   - Opus low is off it on every benchmark: Sonnet high beats it
     everywhere.
   - Opus medium is off on all but FrontierCode.
2. **Haiku stays cheap, but less so.**
   - Haiku pays its 5x rate on almost every request. Even so, it costs
     about half of Sonnet at the same effort: medium 0.10 vs 0.20, high
     0.16 vs 0.33. Haiku medium against Sonnet low, the tiers' neighbours,
     is 0.55 on the owner's requests and 0.46 on CursorBench at list price.
   - On the benchmarks run in Claude Code, it is on the frontier only below
     Sonnet low:
     - Coding Agent Index: Haiku low/medium/high score 28.2/33.6/35.1,
       against 42.1 for Sonnet low. Haiku xhigh is dominated, and Haiku max
       scores below Haiku xhigh.
     - Anthropic's Terminal-Bench: Haiku high scores 24.8 at 0.16, against
       Sonnet medium's 28.8 at 0.20.
   - It takes 2–3x Sonnet's steps and wall time (Coding Agent Index: Haiku
     high 1,107 s vs Sonnet low 381 s).
3. **Opus xhigh** is dominated by Sonnet max on three benchmarks at the
   central cost. But Sonnet max:
   - costs 1.97 pessimistic, and 4.0 when FrontierCode's Claude Code token
     counts are used;
   - scores 46.2 on FrontierCode (vs 51.4) and 55.5 on CursorBench (vs
     56.0);
   - takes 266 steps and 5,245 s a task on the Coding Agent Index.

   It is not a safe replacement. Opus xhigh stays.
4. **Fable 5.1 is dominated everywhere, on both bases.**

## 6. Decisions per scope

### Subagents

**What each level runs.** The criteria stay situations: opus-low's
examples moved into sonnet-high's, and opus-medium's and opus-high's into
sonnet-xhigh's.

| Level | Current config (owner cost) | Candidate (owner cost) | TB 4.0 Anthropic | FrontierCode | CursorBench | AA TB 4.0 | AA index | Verdict |
|---|---|---|---:|---:|---:|---:|---:|---|
| small changes, checks (opus-low) | Opus low (0.47) | Sonnet high (0.33) | +4.5 | +2.1 | +4.1 | +12.6 | +4.4 | merge into sonnet-high |
| design, research (opus-medium) | Opus medium (0.80) | Sonnet xhigh (0.62) | +3.9 | −2.5 | +0.6 | +4.6 | +0.7 | sonnet-xhigh |
| multi-file work with edge cases (opus-high) | Opus high (1.00) | Sonnet xhigh (0.62; 0.83 pess.) | −2.7 | −1.9 | −2.9 | +0.5 | −1.7 | sonnet-xhigh |
| hard debugging, security, concurrency (opus-xhigh) | Opus xhigh (1.61) | Sonnet xhigh (0.62) | −4.9 | +0.7 | −2.9 | −2.5 | −4.1 | Opus stays |
| reading and reporting (sonnet-low) | Sonnet low (0.18) | Haiku high (0.16) | +4.8 | +12.6 | +6.5 | +1.0 | +2.0 | unchanged (see below) |

**Opus high.** This is the judgment call, and the one with the money.
- **Against Opus:** Opus high wins on four of five benchmarks, but by 1.7
  to 2.9 points. That is about one standard error of Terminal-Bench (2.5
  to 2.6 points), so it doesn't clearly win.
- **For Opus:**
  - Anthropic's guidance ("for the hardest long-horizon work, an Opus model
    is the better choice") and SWE-bench Pro at max (Opus 89.9 vs Sonnet
    81.3, the only real-repository eval, and only at max) lean the other
    way.
  - Sonnet xhigh is 1.4–1.5x slower per task: AA Terminal-Bench 1,106 s vs
    806 s; index 431 s vs 286 s.
- **Decision:** applying the owner's own rule (Opus where it clearly wins),
  the level moves.
- **To go back:** a custom tuning with an `opus-high` tier (rank between
  sonnet-xhigh and opus-xhigh) restores it.
- **Why not keep both:** the primary benchmark prices Sonnet xhigh above
  Opus high at list ($2.01 vs $1.82). A Sonnet xhigh tier ranked below Opus
  high would break "costs rise with rank". Keeping Opus high therefore also
  means keeping Opus medium, and the mix then saves 0.5%.

**Sonnet low → Haiku high.** It was considered and not made.
- **For Haiku:** it scores higher on five of six benchmarks, including the
  Coding Agent Index's code Q&A (SWE-Atlas-QnA 46.2 vs 39.0).
- **Against Haiku:**
  - It scores 7 points lower on the Coding Agent Index overall.
  - It is 2.9x slower in Claude Code.
  - It is only 10% cheaper.
  - The level holds $4 a week.

**Mixes compared** (routed subagents, forward, `replay.py`):

| Mix | Central | Pessimistic | Quality (spend-weighted, points: TB4 / FC / CB / AA-TB4 / index) |
|---|---:|---:|---|
| current | $3,745 | $3,745 | — |
| Opus low and medium out | −0.5% | +0.2% | +0.11 / −0.01 / +0.06 / +0.21 / +0.06 |
| **+ Opus high → Sonnet xhigh (proposed)** | **−16.3%** | **−6.3%** | −1.00 / −0.78 / −1.13 / +0.42 / −0.63 |
| + Opus xhigh → Sonnet xhigh | −30.7% | −17.6% | −2.15 / −0.63 / −1.81 / −0.17 / −1.59 |
| Opus high → Opus medium | −8.5% | −8.5% | −2.71 / +0.27 / −1.44 / −1.68 / −0.96 |

**Per level** (central, then pessimistic):
- **opus-high:** $1,536 → $944 ($1,293 pessimistic).
- **opus-medium:** $51 → $41 ($56).
- **opus-low:** $45 → $36 ($48).
- **Unchanged:** opus-xhigh $880, opus-max $1,044, sonnet-high $185, sonnet-low $4.

### Main session

**A second main model doesn't pay back.**

- **What Sonnet would save per request:** the routed main requests would
  cost exactly half on Sonnet at equal tokens, about $0.11 per request.
- **What a switch costs:** it writes the whole context on the other model.
  At the median 556K that is $2.11 more than reading it on Opus, plus the
  stretch's growth written again on Opus on the way back.
- **Why it can't recover that:** low and medium stretches last 10–11
  requests (median), and Sonnet's configs that match Opus low and medium
  take 1.4–1.5x the steps and 2.4–2.6x the output.

| Sonnet for main levels | Every stretch switches | Oracle (only stretches that pay back) |
|---|---:|---:|
| low → Sonnet high | +3.3% (pess. +4.4%) | −0.9% |
| medium → Sonnet xhigh | +5.7% (+7.6%) | −1.0% |
| low + medium | +9.0% (+12.0%) | −1.8% |
| low + medium + high (high → Sonnet xhigh) | +13.5% (+22.0%) | −7.0% |

Percentages are of the routed main spend ($1,216).

- **Even a perfect gate saves under 2% on low and medium.** Low and medium
  hold only 17% of routed main requests.
- **The high level would save more, but only with an oracle.** No routing
  signal knows in advance that a stretch will last.
- **The existing machinery already refuses these switches.** It is
  `SwitchCost` against `Scale` × the expected loss over a 3-prompt horizon.
  At $0.1 per request it never covers $2 in a warm session.
- **So no Sonnet main tier; Opus stays the only scored main model.** Main
  sessions never flip models per prompt. Only the asked Haiku tier, on cold
  turns, changes the model.

**Haiku in the main session** stays an asked tier, on Haiku 5.5 at low.
- **It never touches a warm Opus cache.** It is taken only with no cache to
  lose: a new session, after /compact, or after a pause.
- **It is small.** In the window it served 108 requests (median context
  25K) for $0.56.
- **Low** is Anthropic's advice for chat, short tool tasks and simple
  requests.
- **`max_context` 100K is the price step.** A cold resume of a long session
  goes to Opus, which would rebuild the context anyway at the next real
  prompt.
- **Its cost, 0.024,** is the AA index at low, priced at the ≤100K rate.
- **An Opus conversation with thinking blocks routed to Haiku 5.5 answers
  200** (live harness, 2026-10-07).
- **To check:** the way back (Haiku 5.5's thinking blocks replayed to Opus),
  in a real session (open questions).
- **Code change:** `[effort:X]` on a session on the asked tier now means
  Opus at X, as it did with Haiku 4.5, which had no efforts. Haiku 5.5 has
  efforts, and the tag was being ignored.

### Fable 5.1

Out, as before. It is under Opus high on every per-effort coding benchmark
and costs 1.46–3.28 on the owner's traffic. Its only frontier point anywhere
is AA-LCR at max (+0.7 points).

## 7. Catalog diff

- **Models**
  - **Sonnet 5.5:** cache read 0.10 (was 0.20).
  - **Haiku 5.5:** `scopes = ["main", "subagent"]`.
  - **Haiku 4.5:** `dominated`.
  - Reasons rewritten with the owner-cost figures.
  - Opus, Sonnet and Haiku verified 2026-10-08.
- **Measurements**
  - **v4.3.2 re-read:** Sonnet repriced at $0.10 (−17% to −29%); new
    times and output tokens for Opus, Sonnet and Fable.
  - **Haiku 5.5 index** under `v4.3.2-flat-price`. Its costs are lower
    bounds and must not set tier costs, as `v4.3.2-reasoning` did for Haiku
    4.5.
  - **CursorBench:** Sonnet repriced; steps and tokens for Haiku and Opus.
  - **Coding Agent Index:** Haiku 5.5 rows.
  - **Anthropic's Terminal-Bench:** Sonnet at $0.10 from the Haiku 5.5
    page; Haiku rows.
  - **AA Terminal-Bench:** Sonnet repriced; Opus times; Haiku rows, lower
    bounds.
- **Tiers**
  - **Main haiku:** Haiku 5.5 low, cost 0.024, `max_context` 100K.
  - **Subagents:** haiku, sonnet-low, sonnet-high, sonnet-xhigh,
    opus-xhigh, opus-max. `default_subagent_tier = "sonnet-xhigh"`.
- **Code and docs**
  - The default `budget.max_subagent_tier_when_over` is `sonnet-xhigh`.
    `opus-medium` no longer exists, and a missing tier silently means no
    cap.
  - README examples follow.
  - `[effort:X]` on the asked tier (above).
  - Tests follow the new tiers.
- **Skill**
  - `scripts/replay.py`.
  - SKILL.md step 4 and the sources pitfalls now say to price candidates on
    the ledger.
- **Validation**
  - `frontier.py`: valid, 2 warnings. sonnet-xhigh is dominated by Opus high
    at list on the primary benchmark (expected: section 5). Haiku 5.5 has
    no v4.3.2 measurement (kept apart, above).
  - `catalog check`: 0 errors, the same 2 warnings.

## 8. Eval

**Setup.**

- Dev binary from the worktree, isolated config, 3 repeats.
- The current catalog runs on the current cases. The proposal runs on the
  cases relabelled for the merged tiers: opus-low → sonnet-high, opus-medium
  and opus-high → sonnet-xhigh, in `want` and `accept`, 57 subagent cases.
- 7 new invented train cases cover the new boundaries:
  - small fixes on Sonnet high;
  - design, performance and prototype work on Sonnet xhigh;
  - expensive mistakes on Opus xhigh;
  - reading on Sonnet low.
- Baseline subagent decisions are mapped the same way before comparing.

| | train, current | train, proposed | test, current | test, proposed |
|---|---:|---:|---:|---:|
| main decision exact | 92.6% | 92.7% | 86.2% | 86.6% |
| main decision acceptable | 97.4% | 97.4% | 92.2% | 92.7% |
| main rank error | 0.088 | 0.086 | 0.222 | 0.212 |
| follow-ups below their work | 0 / 1083 | 1 / 1083 | 11 / 267 | 11 / 267 |
| explicit requests confirmed falsely | 8 | 5 | 3 | 3 |
| subagent decision exact (shared cases) | 85.4% | 84.6% | 87.2% | 88.5% |
| subagent decision acceptable | 100% | 100% | 100% | 100% |
| subagent Jev top exact (shared cases) | 86.2% | 79.7% | 84.6% | 84.6% |

**Main scope.**

- Jev is asked the same questions as before: the main criteria, the asked
  tier's question and the model options are unchanged.
- Re-judging the current run's answers with the proposed catalog changes
  no main decision.
- The differences between runs are Jev's variation.
- The one follow-up below its work (`ho-n1-colleague`, low once in 3)
  reads high 5 times out of 5 on both catalogs when re-run. Its relation
  sits at the separate-relation threshold (aside 0.43–0.45).

**Regression gate.** It fails on both catalogs, for failures that predate
this change:
- **Train:** effort and mode requests confirmed on prompts that make none.
  The current catalog has 5 + 3, the proposal 2 + 3, plus that one
  follow-up.
- **Test:** the same seven failures on both. The test split has been read
  since 2026-09-30 and is no longer held out.

**Subagents.**

- The router's decisions hold: all acceptable; exact −1 answer on train,
  +1 case on test.
- Jev's top level is less exact on train with the merged criteria:
  - a branch review read as opus-xhigh ("reviews for subtle bugs");
  - a blind mapping stage read as sonnet-low.
- The policy brings both back to acceptable tiers.
- Decision share on train: opus-xhigh gets 25 of 144 decisions for 18
  labels, all within `accept`.
- The new cases are decided as labelled, except "check where the verbose
  flag goes", which went to sonnet-high (accepted).

Cost: about $0.57 per train run, $0.15 per test run.

## 9. Open questions

1. **Sonnet's steps in Claude Code at xhigh.**
   - This is the main uncertainty in the saving: −16% central, −6%
     pessimistic.
   - The Coding Agent Index has no Opus below max, and its Sonnet rows are a
     pre-release build.
   - To settle it from the ledger after a week: compare requests per agent
     and cost per agent of sonnet-xhigh agents with the opus-high agents of
     this window (median 154 requests, 332K peak), and watch for re-runs or
     follow-up fixes of their work.
2. **The way back from Haiku 5.5 in the main session.** Run a real session
   in the isolated dev harness before merging: a cold Haiku 5.5 turn with a
   thinking block, then a hard prompt on Opus. Check the 200, that the
   thinking is dropped and not refused, and the usage lines.
3. **Haiku's real costs on AA.** AA says it is adding the >100K rate. Once
   it does, Haiku 5.5's index measurements can move to `v4.3.2`.
   CursorBench doesn't say whether it applies the rate.
4. **Tier costs come from list prices on short prompts.** The policy's
   expected loss uses the AA index costs. On this owner's traffic the
   relative costs differ: Haiku medium is 0.55x Sonnet low there, against
   0.41x in the tier costs (0.14 vs 0.345); Sonnet xhigh is 0.39x Opus
   xhigh, against 0.58x ($2.01 vs $3.46). A cost basis taken from the
   ledger (`replay.py --configs`) would fit this owner better. It needs a
   catalog field and its own eval.
5. **Long-lived agents decided at max.**
   - One agent at opus-max cost 17% of all forward subagent spend.
   - Jev's reading was split (high 0.43 / max 0.42), and the penalty
     settled it at max.
   - A cap for agents that live for days, or asking Jev again after N
     hours, is a policy question.
6. **Agents the tiers don't decide** ($2.2K a week):
   - Forks rightly follow the main model.
   - The 57 unrouted agents name a model, or are stages and teammates the
     hooks don't see. To review against the transcripts.
7. **Subscription quota.** Max has per-family weekly limits. Moving work to
   Sonnet spreads it over the Opus and Sonnet buckets. The weight of a
   Sonnet request against an Opus one is not published. The ledger's
   `limits` deltas per model can measure it.
8. **Claude Code 2.1.294 still prices Sonnet 5.5 cache reads at $0.20** in
   `/cost`. It overstates Sonnet's cost by about 20%. This doesn't affect
   automodel, which uses the catalog.
