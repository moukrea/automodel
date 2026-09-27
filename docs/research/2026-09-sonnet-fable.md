# Should Sonnet 5 or Fable get routed? (2026-09-27)

Question from the owner: should Sonnet 5, Fable 5.1 or Fable 5 be routable anywhere, as a main-session tier or as a subagent tier? The bar is strict. A (model, effort) config earns a tier only if it sits on the cost/quality Pareto frontier against Opus 5.5 at some effort, for the kind of work that tier would get.

This was an unattended run of the `refresh-model-catalog` skill. `catalog.toml` was not touched. Every proposed change is in `catalog.proposed.toml`, and a diff is at the end of this report.

## Verdict

**The evidence says Opus 5.5 dominates both scopes for coding work.** No new tier is proposed.

| Model | Main scope | Subagent scope | Key numbers |
|---|---|---|---|
| **Sonnet 5** | **No** | **No** | On CursorBench 4.0 (agentic coding), every effort is dominated by an Opus 5.5 effort. **Sonnet 5 low costs more than Opus 5.5 low**: $1.39 vs $1.17 per task, for 24.1% vs 43.7%. On AA index v4.3.2, Sonnet high costs $1.79 for 32 points, while Opus 5.5 high costs $1.82 for 54. Sonnet low (24, $0.51) is 7% cheaper than Opus 5.5 low (42, $0.55) for −18 points and 1.9× the time. |
| **Fable 5.1** | **No** | **No** | On AA v4.3.2, every effort is dominated: Fable low (47, $2.37) is beaten by Opus 5.5 medium (51, $1.34), and Fable max (53, $7.63) by Opus 5.5 xhigh (56, $3.46). On CursorBench 4.0, Fable's best config (max, 51.8%, $17.28) scores below Opus 5.5 medium (52.5%, $2.91). Anthropic's own system card puts it below Opus 5.5 on every coding eval. |
| **Fable 5** | **No** | **No** (the `fable` alias can't reach it) | Legacy. On AA v4.3.2, max scores 50 for $8.75, vs Opus 5.5 medium at 51 for $1.34, so it costs 6.5× as much. Cache reads cost $1/M, 5× Opus 5.5. It has no per-message effort. |
| Opus 5 / Opus 4.8 | No | No | Figures are now recorded. Opus 5 is dominated at every effort. Opus 4.8 max scores 42 for $4.08, the same index Opus 5.5 low reaches for $0.55. |
| Haiku 4.5 | n/a (200K window) | Keep, unchanged | Now measured: 17 for $0.28 per task (reasoning variant), 187 s. That puts it on the frontier by cost only, at 51% of Opus 5.5 low's cost, but it is 2.3× slower. |

There is one nuance, and it does not change the verdict. On the **AA Coding Agent Index v1.5** (run in Claude Code, max effort only), Fable 5.1 max is *not* dominated by Opus 5.5 max:

- Fable 5.1 max: 62.2 for $12.39 per task, 2090 s wall time.
- Opus 5.5 max: 66.0 for $13.04 per task, 3867 s wall time.

So Fable 5.1 max is 5% cheaper and 46% faster, for −3.8 points. It would still not earn a tier, for three reasons:

- **Quality.** It is never *better* than Opus 5.5. At the top of the ladder, where cost is secondary, Opus 5.5 max wins.
- **Cost.** As a cheaper step it saves only 5%. Opus 5.5 xhigh very probably gets it much cheaper:
  - On CursorBench, Opus xhigh scores 56.0% for $6.98 against Fable max's 51.8% for $17.28.
  - On Terminal-Bench 4.0, Opus xhigh scores 66.4 against Fable max's 55.8.
  - Opus 5.5 xhigh has not been measured on this index yet (open question 1).
- **Reliability.** 8.1% of Fable's attempts on that index hit the safety fallback (74/909).

## What changed since 2026-09-26

- **Prices: unchanged.** They were re-read on the pricing page on 2026-09-27. The Fable prices were added.
- **AA index v4.3.2 now publishes Sonnet 5 high and xhigh**, and Fable 5.1 at all five efforts.
- **AA revised several time-per-task figures.** Opus 5.5 high went from 262 to 284 s, xhigh from 458 to 468 s, and Sonnet max from 862 to 997 s. The catalog now uses the current values.
- **Haiku 4.5 now has a v4.3.2 measurement.** The "active without a measurement" warning is gone.
- **Two agentic-coding benchmarks with per-task cost are now recorded under their own `benchmark_version`.** They are never compared with v4.3.2, and frontier.py ignores them unless `--version` is passed:
  - `cursorbench-4.0` (Cursor, 2026-09-10);
  - `aa-coding-agent-index-v1.5` (Claude Code harness).

## 1. Prices

Source: https://platform.claude.com/docs/en/about-claude/pricing, fetched 2026-09-27. Prices are in $/MTok.

| Model | Input | Output | Cache read | Cache write 5m | Cache write 1h |
|---|---:|---:|---:|---:|---:|
| Opus 5.5 | 4 | 20 | **0.20** (0.05×) | 5 | 8 |
| Sonnet 5 | 2 | 10 | **0.20** (0.1×) | 2.50 | 4 |
| Fable 5.1 | 10 | 50 | 0.25 (0.025×) | 12.50 | 20 |
| Fable 5 | 10 | 50 | 1.00 (0.1×) | 12.50 | 20 |
| Haiku 4.5 | 1 | 5 | 0.10 | 1.25 | 2 |
| Opus 5 / Opus 4.8 | 5 | 25 | 0.50 | 6.25 | 10 |

Pricing notes:

- **Sonnet 5.** The $2/$10 price, announced as introductory, "is now the standard price". The scheduled rise to $3/$15 on 2026-09-01 "will not occur".
- **Opus 5.5 fast mode:** $8/$40.
- **Tokenizers.** Every model above except Haiku 4.5 uses the newer tokenizer ("~30% more tokens" than the one before 4.7). The comparisons below are per task, so this doesn't bias them.

## 2. Context windows, output, effort, per-turn effort

Sources:

- Models overview: https://platform.claude.com/docs/en/about-claude/models/overview (2026-09-27)
- Effort: https://platform.claude.com/docs/en/build-with-claude/effort (2026-09-27)
- Claude Code 2.1.283's embedded catalog: `strings $(readlink -f $(which claude))`, read 2026-09-27

| Model | Window | 1M | Max output | Efforts | Default | Per-turn effort (API) | `per_turn_effort` in Claude Code |
|---|---|---|---|---|---|---|---|
| Opus 5.5 | 1M | native (`native_1m:!0`) | 128K | low→max | medium | yes | **yes** |
| Sonnet 5 | 1M | native | 128K (CC default 64K, upper 128K) | low→max | high | **no** (only Fable 5.1, Mythos 5.1, Opus 5.5, Opus 5) | **no** |
| Fable 5.1 | 1M | native | 128K (CC 64K/128K) | low→max | high | yes | yes |
| Fable 5 | 1M | native | 128K | low→max | high | **no** (400 error) | no |
| Haiku 4.5 | 200K | — | 64K | none (extended thinking) | — | no | no |

Claude Code aliases (`latest_per_family`) resolve as follows:

- `fable` → `claude-fable-5-1`
- `sonnet` → `claude-sonnet-5`
- `haiku` → `claude-haiku-4-5`
- `opus` → `claude-opus-5-5`

Fable 5 therefore can't be reached as a subagent.

Claude Code also embeds an `effort_cost_index` for each model: the relative cost of each effort, with high = 1.

- **Sonnet 5:** low 0.47, medium 0.74, xhigh 2.41, max 5.59.
- **Fable 5.1:** low 0.75, medium 0.86, xhigh 1.38, max 1.74. **Lowering effort on Fable saves little.** AA shows the same thing: Fable low costs 61% of Fable high.
- **Fable 5:** low 0.60, medium 0.77, xhigh 1.74, max 1.91.

There is none for Opus 5.5.

Every candidate meets the ≥1M main-scope minimum natively, so context is not what disqualifies them. What disqualifies them for the main scope, before any cost argument:

- **Sonnet 5 lacks per-turn effort in Claude Code.** Every effort change on it rewrites the conversation cache.
- **A main-model switch loses the prompt cache and the thinking blocks.** Both the Opus 5.5 and Fable 5.1 docs say "thinking blocks are tied to the model that produced them". Take a routed switch from Opus 5.5 at 500K context. At the 5-minute write price, rewriting that context costs:
  - $6.25 on Fable 5.1;
  - $1.25 on Sonnet 5;
  - about 51 tokens (effectively $0) for a per-turn effort change on Opus 5.5 (docs/spikes.md S10–S11).

## 3. Quality per (model, effort)

### 3a. AA Intelligence Index v4.3.2: the catalog's benchmark

The index mixes knowledge work and agentic terminal work, and includes Terminal-Bench 4.0. Sources, all read 2026-09-27:

- https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5
- https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1
- https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-4-5-haiku
- https://artificialanalysis.ai/models/releases/claude-fable-5
- https://artificialanalysis.ai/models/releases/claude-opus-5
- https://artificialanalysis.ai/models/releases/claude-opus-4-8

| Config | Index | $/task | s/task | Output tok/task | Output total (index) | Output speed | Status |
|---|---:|---:|---:|---:|---:|---:|---|
| Haiku 4.5 (reasoning) | 17 | 0.28 | 187 | 18k | 78M | 81 t/s | frontier (cost only) |
| Sonnet 5 low | 24 | 0.51 | 157 | — | 25M | 58 t/s | frontier, quasi-dominated |
| **Opus 5.5 low** | **42** | **0.55** | **81** | 10k | 20M | 79 t/s | frontier |
| Sonnet 5 medium | 28 | 1.00 | 276 | — | 51M | 65 t/s | dominated by Opus 5.5 low |
| Opus 5 low | 39 | 1.10 | — | — | — | 60 t/s | dominated |
| **Opus 5.5 medium** | **51** | **1.34** | **203** | 26k | 38M | 81 t/s | frontier |
| Sonnet 5 high | 32 | 1.79 | 404 | — | 89M | 66 t/s | dominated |
| **Opus 5.5 high** | **54** | **1.82** | **284** | 36k | 53M | 81 t/s | frontier |
| Opus 5 medium | 45 | 2.19 | — | — | — | 57 t/s | dominated |
| Fable 5.1 low | 47 | 2.37 | 245 | 22k | 33M | 56 t/s | dominated by Opus 5.5 medium |
| Sonnet 5 xhigh | 34 | 2.87 | 597 | — | 135M | 69 t/s | dominated |
| Fable 5.1 medium | 49 | 2.98 | 318 | 28k | 44M | 57 t/s | dominated |
| **Opus 5.5 xhigh** | **56** | **3.46** | **468** | 66k | 100M | 88 t/s | frontier |
| Opus 5 high | 48 | 3.61 | — | — | — | 59 t/s | dominated |
| Fable 5.1 high | 51 | 3.91 | 445 | 38k | 62M | 56 t/s | dominated |
| Opus 4.8 max | 42 | 4.08 | — | — | 170M | 53 t/s | dominated |
| Opus 5 xhigh | 50 | 4.88 | — | — | — | 58 t/s | dominated |
| Sonnet 5 max | 38 | 5.09 | 997 | — | 367M | 75 t/s | dominated |
| Opus 5 max | 51 | 5.86 | — | — | — | 59 t/s | dominated |
| **Opus 5.5 max** | **58** | **5.98** | — | 119k | 260M | — | frontier |
| Fable 5.1 xhigh | 53 | 5.98 | 646 | 61k | 121M | 65 t/s | dominated |
| Fable 5.1 max | 53 | 7.63 | 711 | 78k | 188M | 68 t/s | dominated |
| Fable 5 max | 50 | 8.75 | — | — | 130M | 64 t/s | dominated |

Cost to run the whole index:

- **Opus 5.5:** $860 (low), $1,627 (medium), $2,172 (high), $4,057 (xhigh), $8,708 (max).
- **Fable 5.1:** $3,158 (low), $3,983 (medium), $5,242 (high), $9,063 (xhigh), $13,129 (max).

Two numbers come from other versions of the index and must not be compared with the table above:

- AA's Fable 5.1 launch article (2026-09-01) quotes "66 at max, $3.76/task". It was written on an earlier index version; v4.3.2 gives 53 and $7.63.
- The Sonnet 5 launch article (2026-06-30) quotes "53, $2.29/task". It uses an older version too, and was written at the old $3/$15 price.

**Marginal returns along the v4.3.2 frontier** (from `frontier.py catalog.proposed.toml`):

| Step | Index gain | Cost multiplier |
|---|---:|---:|
| Haiku 4.5 → Sonnet 5 low | +7 | ×1.82 |
| Sonnet 5 low → Opus 5.5 low | +18 | ×1.08 |
| Opus 5.5 low → medium | +9 | ×2.44 |
| Opus 5.5 medium → high | +3 | ×1.36 |
| Opus 5.5 high → xhigh | +2 | ×1.90 |
| Opus 5.5 xhigh → max | +2 | ×1.73 |

Sonnet 5 low is technically on the v4.3.2 frontier, but frontier.py flags it as quasi-dominated: 7.3% cheaper than Opus 5.5 low for −18 points. It also takes 1.9× the time per task. Skipping it costs 8% and gains 18 points, and that is before the coding benchmark below, where Sonnet low costs *more*.

### 3b. CursorBench 4.0: agentic coding in a production harness

Source: https://cursor.com/cursorbench (leaderboard dated 2026-09-10, read 2026-09-27). Cost per task is computed at list prices, including cache reads and writes. The Opus 5.5 costs are Anthropic's estimates from Cursor's token counts (Opus 5.5 system card §8.8); the other costs are Cursor's own.

| Config | Score | $/task | Tokens/task | Steps/task | Status |
|---|---:|---:|---:|---:|---|
| **Opus 5.5 low** | **43.7%** | **1.17** | 15,811 | 28 | frontier |
| Sonnet 5 low | 24.1% | **1.39** | 23,772 | 46 | dominated by Opus 5.5 low |
| Sonnet 5 medium | 28.0% | 2.31 | 39,114 | 65 | dominated by Opus 5.5 low |
| **Opus 5.5 medium** | **52.5%** | **2.91** | 37,954 | 54 | frontier |
| Sonnet 5 high | 30.8% | 3.48 | 61,146 | 85 | dominated |
| **Opus 5.5 high** | **56.0%** | **3.97** | 53,078 | 68 | frontier |
| Sonnet 5 xhigh | 32.0% | 4.55 | 83,373 | 102 | dominated |
| Opus 5 low | 40.7% | 4.87 | 31,995 | 57 | dominated (not recorded) |
| Fable 5.1 low | 45.1% | 5.44 | 34,795 | 51 | dominated by Opus 5.5 medium |
| Opus 5.5 xhigh | 56.0% | 6.98 | 101,083 | 109 | dominated by Opus 5.5 high |
| Fable 5.1 medium | 46.8% | 7.05 | 45,411 | 63 | dominated |
| Sonnet 5 max | 34.1% | 7.17 | 149,257 | 140 | dominated |
| Fable 5.1 high | 49.2% | 9.08 | 58,438 | 77 | dominated |
| Opus 5 max | 46.6% | 11.95 | 85,384 | 106 | dominated (not recorded) |
| Fable 5.1 xhigh | 51.6% | 13.01 | 87,294 | 101 | dominated |
| **Opus 5.5 max** | **57.8%** | **13.43** | 218,363 | 185 | frontier |
| Fable 5.1 max | 51.8% | 17.28 | 117,236 | 128 | dominated |

Marginal returns:

- low → medium: +8.8 for ×2.49.
- medium → high: +3.5 for ×1.36.
- high → max: +1.8 for ×3.38.

On this benchmark, **Opus 5.5 xhigh gains nothing over high** (56.0 vs 56.0) at 1.76× the cost. On AA v4.3.2 (+2) and Terminal-Bench 4.0, xhigh does gain, so no tier change is proposed. The existing xhigh criteria ("a wrong answer is expensive") are already restrictive.

### 3c. AA Coding Agent Index v1.5: Claude Code harness, max effort only

Source: https://artificialanalysis.ai/agents/coding-agents. The embedded data was materialized 2026-09-26 and read 2026-09-27. The index is DeepSWE v1.1, Terminal-Bench 4.0 and SWE-Atlas-QnA, averaged.

| Config (Claude Code) | Index | $/task | Wall s/task | Steps | Output tok | Input tok | Cache hit | DeepSWE | TB 4.0 | SWE-Atlas-QnA | Safety fallback |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Opus 5 max | 59.7 | 10.79 | 2516 | 152 | 137k | 11.2M | 0.97 | 62.5 | 54.5 | 62.1 | 3/908 |
| Fable 5.1 max | 62.2 | 12.39 | 2090 | 37 | 134k | 5.6M | 0.92 | 64.3 | 57.6 | 64.8 | **74/909** |
| Opus 5.5 max | 66.0 | 13.04 | 3867 | 155 | 333k | 15.2M | 0.95 | 68.4 | 63.1 | 66.4 | 78/909 |

All three configs are on this index's frontier. Only max effort was measured, and neither Sonnet 5 nor Haiku 4.5 has a Claude Code row.

This is the one place Fable 5.1 isn't dominated. Its profile is different: it takes 4× fewer steps and has 2.7× fewer input tokens, but it is still 8.4× more expensive per input token. See the verdict above for why it earns no tier.

### 3d. Anthropic system cards

**Opus 5.5 system card**, Table 8.1.A (https://www.anthropic.com/claude-opus-5-5-system-card, published 2026-09-22, read 2026-09-27). Opus 5.5 runs at max effort unless noted; the comparison models run at max.

| Eval | Opus 5.5 | Opus 5 | Fable 5.1 |
|---|---:|---:|---:|
| SWE-bench Pro | 89.9 | 79.2 | 81.2 |
| SWE-bench Multilingual | 93.9 | 89.5 | 89.1 |
| FrontierCode v1.1 Main | 54.4 (54.6 at medium, its best) | 48.0 | 50.3 |
| Terminal-Bench 4.0 | 66.4 (xhigh; 64.8 at max) | 52.3 | 55.8 |
| CursorBench 4.0 | 57.8 (high 56.0 for ~$4) | 46.6 ($11.95) | 51.8 ($17.28) |
| FrontierSWE v2 | 62.3 | — | 56.3 |

**Sonnet 5 system card**, Table 8.1.A (https://www.anthropic.com/claude-sonnet-5-system-card, published 2026-06-30). Sonnet runs at max effort unless noted.

- SWE-bench Verified: 85.2.
- SWE-bench Pro: 63.2.
- Terminal-Bench **2.1**: 80.4 at xhigh.
- FrontierCode **v1**: 38.8.

Two caveats on these numbers:

- **The evaluation setups differ.** The Opus 5.5 card doesn't include Sonnet 5. Its SWE-bench Pro figures (79–90) are far above Sonnet's 63.2, which suggests a different setup. The two cards should not be compared directly.
- **The versions differ.** Terminal-Bench 2.1 and FrontierCode v1 are older than the 4.0 and v1.1 versions used above.

The only apples-to-apples coding comparisons that include Sonnet 5 are CursorBench 4.0 and AA v4.3.2 (which includes Terminal-Bench 4.0).

**Haiku 4.5** has no measurement on CursorBench 4.0 or on the Coding Agent Index. Its only same-version measurement is AA v4.3.2:

- reasoning variant: 17, $0.28 per task;
- non-reasoning variant: 15, with no published cost.

Claude Code sends `thinking: {type:"enabled", budget_tokens}` to models without adaptive thinking when thinking is on, so the reasoning variant is the closer match.

## 4. Latency and speed

| | Opus 5.5 | Sonnet 5 | Fable 5.1 | Haiku 4.5 |
|---|---|---|---|---|
| Anthropic "comparative latency" | Moderate | Fast | Slower | Fastest |
| AA output speed (v4.3.2 runs) | 79–88 t/s | 58–75 t/s | 56–68 t/s | 81 t/s |
| AA time/task, low | 81 s | 157 s | 245 s | 187 s (reasoning) |
| AA time/task, medium | 203 s | 276 s | 318 s | — |
| AA time/task, high | 284 s | 404 s | 445 s | — |
| AA TTFT, low / high | 6.1 s / 39.1 s | 1.3 s / 5.5 s | 6.5 s / 11.7 s | 22.5 s (reasoning), 0.65 s (non-reasoning) |
| Coding Agent Index wall time (max) | 3867 s | — | 2090 s | — |

**Sonnet 5 is "Fast" per token of time-to-first-token, but slower per task than Opus 5.5 at every effort.** It generates more tokens at a lower output speed. Fable 5.1 is slower per task on AA, but faster wall-clock at max in Claude Code, where it takes 37 steps against Opus's 155.

## 5. Where Sonnet or Fable cost more than Opus for the same work

1. **Sonnet 5 low costs 19% more than Opus 5.5 low on CursorBench** ($1.39 vs $1.17), for 24.1% vs 43.7%. It used 1.50× the tokens (23.8k vs 15.8k) and 1.64× the steps (46 vs 28).
2. **Sonnet 5 at a given score costs more than Opus 5.5 at a higher score:**
   - Sonnet high ($3.48, 30.8%) costs more than Opus medium ($2.91, 52.5%).
   - Sonnet max ($7.17, 34.1%) costs more than Opus xhigh ($6.98, 56.0%).
   - On AA, Sonnet high ($1.79) costs the same as Opus high ($1.82) for 32 vs 54 points, and 34% more than Opus medium ($1.34, 51).
3. **Sonnet is more verbose.** On AA v4.3.2 it emits 1.25× (low), 1.34× (medium), 1.68× (high), 1.35× (xhigh) and 1.41× (max) Opus 5.5's output tokens at equal effort. AA's Sonnet 5 launch analysis (older index version) found about 40% more output tokens than Sonnet 4.6 and "roughly triple the agentic turns".
4. **Cache-read parity erases Sonnet's price advantage in Claude Code sessions.** This user's ledger (`automodel report --json`, 2026-09-27) shows the cost structure:
   - Opus 5.5 traffic: 2.18B cache-read tokens, 38.6M cache-write, 1.25M uncached input, 4.53M output.
   - Routed cache hit rate: 98.3%.
   - At Opus 5.5 prices, that traffic cost **$724**, and cache reads were 60% of it.
   - At Sonnet 5 prices, the *same tokens* would cost **$580 (−20%)**. Cache reads cost the same on both models, so most of the bill grows with steps × context. **If Sonnet needs ≥1.25× Opus's steps, it costs more.** On CursorBench it needed 1.64× (low), 1.20× (medium) and 1.25× (high).
5. **Fable 5.1 costs 1.75× on the same tokens.** The same ledger tokens at Fable 5.1 prices come to **$1,266**:
   - cache read $0.25 vs $0.20;
   - cache write $12.50 vs $5;
   - output $50 vs $20.

   On CursorBench, Fable's cheapest config (low, $5.44) costs 1.87× Opus 5.5 medium ($2.91) and scores 7.4 points lower. Its max ($17.28) costs 5.9× Opus medium for a lower score.
6. **Lowering effort barely saves on Fable.** Claude Code's `effort_cost_index` puts Fable 5.1 low at 0.75 of Fable high. On AA, Fable low costs $2.37: that is 4.3× Opus 5.5 low, and 1.77× Opus 5.5 medium, which scores higher (51 vs 47).
7. **Fable 5's cache reads cost $1/M**, 5× Opus 5.5. In a cache-dominated session, that alone would roughly double or triple the bill.
8. **Switching to either model costs a full cache rewrite.** At 500K context:
   - Fable 5.1: $6.25 (5-minute write);
   - Sonnet 5: $1.25;
   - Opus 5.5 per-turn effort change: about $0.

   Sonnet 5 has no per-turn effort, so it would also pay the rewrite on every effort change.

## 6. Per-scope reasoning

**Main scope** (one model; only effort varies):

- **Structural.** The main model should support per-turn effort and stay the same model all session. Sonnet 5 fails the first condition in Claude Code, and any second model fails the second.
- **Frontier.** A second main model would be justified only if some config clearly sat on the frontier where Opus 5.5 has a gap. No such config exists on AA v4.3.2 or on CursorBench 4.0. The only gap-filler is Sonnet 5 low on AA: −18 points to save $0.04 per task, and it costs more on CursorBench.
- **Verdict: No for Sonnet 5, Fable 5.1 and Fable 5.**

**Subagent scope** (aliases only; the Agent tool has no effort field, and effort comes from the proxy/workflow):

- **Where Sonnet would slot.** A `sonnet` tier would sit between `haiku` (17, $0.28) and `opus-low` (42, $0.55). The only candidate is Sonnet 5 low (24, $0.51, 157 s): +7 over Haiku at 1.82× the cost, and 7% cheaper than Opus 5.5 low for −18. On agentic coding (CursorBench) it is dominated outright. Subagent work in this ledger is mostly `opus-xhigh`/`opus-high` (26 of 39 decisions), where Sonnet has nothing to offer.
- **Where Fable would slot.** A `fable` tier would sit at the top. At max in Claude Code it is 5% cheaper and faster than Opus 5.5 max, but lower quality. Everywhere its full effort ladder is measured, it is dominated.
- **Verdict: No for Sonnet 5 and Fable 5.1.** Fable 5 is unreachable (`fable` → 5.1).

**Haiku 4.5 stays in its tier.** Its cost now comes from the measurement ($0.28), replacing the price-ratio estimate (0.15).

## 7. Router impact of `catalog.proposed.toml`

- **Tiers, modes, criteria, thresholds and defaults: unchanged.** No new tier.
- **The subagent `haiku` tier's cost rises from 0.15 to 0.28**, because it now comes from the measurement. Relative to `opus-low` (0.55), Haiku goes from 27% to 51% of the cost, so the router's expected saving from dropping to Haiku halves. That is a real behaviour change: Jev will pick Haiku slightly less often at the margin. Keep the old `cost = 0.15` if you disagree (open question 3).
- **Model entries.**
  - Sonnet 5, Fable 5.1, Fable 5, Opus 5 and Opus 4.8 are all `dominated`, with numeric reasons.
  - Fable 5.1 moves from `excluded` to `dominated`.
  - Fable 5 is new.
  - Fable and Opus-family entries gain `api_id`, `price` and `efforts`.
  - Sonnet gains `default_effort = "high"`.
- **Opus 5.5 v4.3.2 times updated** (high 262 → 284 s, xhigh 458 → 468 s, low 80 → 81 s), and `output_tokens_per_task` added. The high, xhigh and max main tiers' Jev cost inputs are unchanged (cost per task is unchanged).
- **Validation.**
  - `meta.last_refresh` is left at 2026-09-26. It gets set on approval, per the skill.
  - `frontier.py catalog.proposed.toml` exits 0 and reports valid with no warnings.
  - `go run ./cmd/automodel catalog check --catalog catalog.proposed.toml` reports 0 errors and 0 warnings. The current `catalog.toml` has 1 warning (Haiku unmeasured).
- **Nothing touches betas, `long_context` or `per_turn_effort` on routed models.** No real-session check (step 8) is needed.

## Open questions

1. **Opus 5.5 xhigh/high on the Coding Agent Index.** Only max is published. If Opus 5.5 xhigh scores ≥62.2 for less than $12.39 (likely, given CursorBench and Terminal-Bench 4.0), Fable 5.1 max is dominated there too. If it isn't, reconsider Fable only for long-running, wall-clock-sensitive work. *Settle by* re-reading https://artificialanalysis.ai/agents/coding-agents at the next refresh.
2. **Sonnet 5 and Haiku 4.5 on a Claude Code harness.** Neither is on the Coding Agent Index, and Haiku isn't on CursorBench. *Settle by* a ledger A/B: a week of subagent `haiku` decisions compared with `opus-low` on escalations and rework. The ledger wins over benchmarks for this user.
3. **Haiku's cost basis.** The AA reasoning variant ($0.28) is assumed to match Claude Code (extended thinking on). If Claude Code subagents run Haiku without thinking, the true cost is lower (index 15, no published cost). *Settle by* reading `thinking` on a routed Haiku subagent request in `ledger.jsonl`. If thinking is absent, keep `cost = 0.15` or find a non-reasoning cost.
4. **Opus 5 per-turn effort mismatch.** The API docs say Opus 5 supports per-message effort; Claude Code 2.1.283's catalog doesn't list `per_turn_effort` for it. This doesn't matter while Opus 5 is dominated. Flagging it in case it's routed again.
5. **CursorBench cost methodology.** The Opus 5.5 costs are Anthropic's estimates from Cursor's token counts; the other models' costs are Cursor's. The gaps are large enough (for example Opus medium $2.91 vs Fable max $17.28) that the verdict is robust to this.
6. **SWE-bench Pro comparability.** Sonnet 5 scores 63.2 (June card) and Opus 5.5 89.9 (September card). The setups are likely different, so the two numbers weren't used for any decision.

## Sources (all read 2026-09-27)

**Anthropic**

- Pricing: https://platform.claude.com/docs/en/about-claude/pricing
- Models overview: https://platform.claude.com/docs/en/about-claude/models/overview
- Model pages: https://platform.claude.com/docs/en/models/opus-5-5/overview, https://platform.claude.com/docs/en/models/sonnet-5/overview, https://platform.claude.com/docs/en/models/fable-5-1/overview and https://platform.claude.com/docs/en/models/fable-5/overview
- Effort: https://platform.claude.com/docs/en/build-with-claude/effort
- Opus 5.5 announcement: https://www.anthropic.com/claude-opus-5-5 (2026-09-22)
- Fable 5.1 announcement: https://www.anthropic.com/claude-fable-and-mythos-5-1 (2026-09)
- Sonnet 5 announcement: https://www.anthropic.com/news/claude-sonnet-5 (2026-06-30)
- System cards:
  - Opus 5.5: https://www.anthropic.com/claude-opus-5-5-system-card
  - Sonnet 5: https://www.anthropic.com/claude-sonnet-5-system-card
  - Fable 5.1 / Mythos 5.1: https://www.anthropic.com/claude-fable-5-1-mythos-5-1-system-card

**Artificial Analysis**

- Release comparisons:
  - https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5
  - https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1
  - https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-4-5-haiku
- Release pages: https://artificialanalysis.ai/models/releases/claude-fable-5, https://artificialanalysis.ai/models/releases/claude-opus-5 and https://artificialanalysis.ai/models/releases/claude-opus-4-8
- Coding Agent Index: https://artificialanalysis.ai/agents/coding-agents
- Articles on older index versions (context only, not compared):
  - https://artificialanalysis.ai/articles/claude-fable-5-1
  - https://artificialanalysis.ai/articles/claude-sonnet-5-agentic-cost
  - https://artificialanalysis.ai/articles/claude-opus-5-5

**Cursor**

- https://cursor.com/cursorbench (CursorBench 4.0, 2026-09-10)

**Local**

- Claude Code 2.1.283 binary (`/home/emeric/.local/share/claude/versions/2.1.283`): `first_party:"claude-…"` catalog entries, `TIER_DESCRIPTIONS`, `latest_per_family`
- `automodel report --json`

## Diff

```diff
--- catalog.toml	2026-09-26 21:37:18.554965674 +0200
+++ catalog.proposed.toml	2026-09-27 15:09:52.095313855 +0200
@@ -43,16 +43,18 @@
 per_turn_effort = true
 price = { input = 4.0, output = 20.0, cache_read = 0.20, cache_write_5m = 5.0, cache_write_1h = 8.0 }
 fast = { allowed = false, input = 8.0, output = 40.0, speedup = 2.5 }
-verified_at = "2026-09-26"
+verified_at = "2026-09-27"
 sources = [
   "https://platform.claude.com/docs/en/about-claude/models/overview",
   "https://platform.claude.com/docs/en/about-claude/pricing",
+  "https://platform.claude.com/docs/en/build-with-claude/effort",
+  "Claude Code 2.1.283 embedded model catalog (native_1m, per_turn_effort)",
 ]
 
 [models.claude-haiku-4-5]
 label = "Haiku 4.5"
 status = "active"
-reason = "Not measured on index v4.3.2; kept for mechanical subagent work pending ledger data."
+reason = "On the index v4.3.2 frontier by cost only: 17 at $0.28/task (reasoning variant; Claude Code enables extended thinking on it) vs Opus 5.5 low 42 at $0.55, but 2.3x slower (187 s vs 81 s per task). 200K window: subagent scope only. No agentic-coding measurement (absent from CursorBench 4.0 and the Coding Agent Index); the ledger decides."
 api_id = "claude-haiku-4-5-20251001"
 alias = "haiku"
 context = 200_000
@@ -60,51 +62,113 @@
 efforts = []
 price = { input = 1.0, output = 5.0, cache_read = 0.10, cache_write_5m = 1.25, cache_write_1h = 2.0 }
 scopes = ["subagent"]
-verified_at = "2026-09-26"
-sources = ["https://platform.claude.com/docs/en/about-claude/pricing"]
+verified_at = "2026-09-27"
+sources = [
+  "https://platform.claude.com/docs/en/about-claude/pricing",
+  "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-4-5-haiku",
+]
 
 [models.claude-sonnet-5]
 label = "Sonnet 5"
 status = "dominated"
-reason = "Every effort dominated by Opus 5.5 low (index 38 max vs 42); low is quasi-dominated (7% cheaper than Opus 5.5 low for -18 points). Cache read costs the same as Opus 5.5 ($0.20/M)."
+reason = "Dominated by Opus 5.5 for coding work. Index v4.3.2: medium to max (28 to 38, $1.00 to $5.09) dominated by Opus 5.5 low/medium/high; high costs $1.79, the same as Opus 5.5 high ($1.82), for 32 vs 54. Low (24, $0.51, 157 s) is only 7% cheaper than Opus 5.5 low (42, $0.55, 81 s) for -18 points and 1.9x the time. CursorBench 4.0 (agentic coding): every effort dominated; low costs more than Opus 5.5 low ($1.39 vs $1.17 for 24.1% vs 43.7%, 1.5x the tokens, 1.6x the steps). Emits 1.25-1.68x Opus 5.5's output tokens at equal effort; cache read costs the same ($0.20/M); no per-turn effort in Claude Code."
 api_id = "claude-sonnet-5"
 alias = "sonnet"
 context = 1_000_000
 long_context = "native"
 max_output = 128_000
 efforts = ["low", "medium", "high", "xhigh", "max"]
+default_effort = "high"
 price = { input = 2.0, output = 10.0, cache_read = 0.20, cache_write_5m = 2.50, cache_write_1h = 4.0 }
-verified_at = "2026-09-26"
-sources = ["https://platform.claude.com/docs/en/about-claude/pricing"]
+verified_at = "2026-09-27"
+sources = [
+  "https://platform.claude.com/docs/en/about-claude/pricing",
+  "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5",
+  "https://cursor.com/cursorbench",
+]
+
+[models.claude-fable-5-1]
+label = "Fable 5.1"
+status = "dominated"
+reason = "Dominated by Opus 5.5. Index v4.3.2: low 47 at $2.37 and high 51 at $3.91 vs Opus 5.5 medium 51 at $1.34; xhigh and max 53 at $5.98/$7.63 vs Opus 5.5 xhigh 56 at $3.46. CursorBench 4.0: its best config (max, 51.8%, $17.28) scores below Opus 5.5 medium (52.5%, $2.91). Anthropic's Opus 5.5 system card: below Opus 5.5 on every coding eval (Terminal-Bench 4.0 55.8 vs 66.4, SWE-bench Pro 81.2 vs 89.9). Its only non-dominated point, the Coding Agent Index v1.5 at max in Claude Code (62.2, $12.39, 2090 s vs Opus 5.5 max 66.0, $13.04, 3867 s), is 5% cheaper and lower quality, which is no reason to route to it. List prices 2.5x Opus 5.5; cache read $0.25/M vs $0.20/M."
+api_id = "claude-fable-5-1"
+alias = "fable"
+context = 1_000_000
+long_context = "native"
+max_output = 128_000
+efforts = ["low", "medium", "high", "xhigh", "max"]
+default_effort = "high"
+per_turn_effort = true
+price = { input = 10.0, output = 50.0, cache_read = 0.25, cache_write_5m = 12.50, cache_write_1h = 20.0 }
+verified_at = "2026-09-27"
+sources = [
+  "https://platform.claude.com/docs/en/about-claude/pricing",
+  "https://platform.claude.com/docs/en/models/fable-5-1/overview",
+  "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1",
+  "https://cursor.com/cursorbench",
+  "https://artificialanalysis.ai/agents/coding-agents",
+  "https://www.anthropic.com/claude-opus-5-5-system-card",
+]
+
+[models.claude-fable-5]
+label = "Fable 5"
+status = "dominated"
+reason = "Legacy, superseded by Fable 5.1. Index v4.3.2 (max, the only config measured): 50 at $8.75/task vs Opus 5.5 medium 51 at $1.34 (6.5x the cost). Cache read $1/M (5x Opus 5.5). No per-message effort (the API returns 400). The `fable` alias resolves to Fable 5.1, so subagents can't reach it."
+api_id = "claude-fable-5"
+context = 1_000_000
+long_context = "native"
+max_output = 128_000
+efforts = ["low", "medium", "high", "xhigh", "max"]
+default_effort = "high"
+price = { input = 10.0, output = 50.0, cache_read = 1.0, cache_write_5m = 12.50, cache_write_1h = 20.0 }
+verified_at = "2026-09-27"
+sources = [
+  "https://platform.claude.com/docs/en/about-claude/pricing",
+  "https://platform.claude.com/docs/en/models/fable-5/overview",
+  "https://artificialanalysis.ai/models/releases/claude-fable-5",
+]
 
 [models.claude-opus-5]
 label = "Opus 5"
 status = "dominated"
-reason = "Superseded by Opus 5.5 at equal or lower cost per task; figures to record at the first refresh."
+reason = "Dominated by Opus 5.5 at every effort. Index v4.3.2: low 39 at $1.10 vs Opus 5.5 low 42 at $0.55; medium to max (45 to 51, $2.19 to $5.86) vs Opus 5.5 medium 51 at $1.34. CursorBench 4.0 max: 46.6% at $11.95 vs Opus 5.5 medium 52.5% at $2.91."
+api_id = "claude-opus-5"
+efforts = ["low", "medium", "high", "xhigh", "max"]
+price = { input = 5.0, output = 25.0, cache_read = 0.50, cache_write_5m = 6.25, cache_write_1h = 10.0 }
+verified_at = "2026-09-27"
+sources = [
+  "https://platform.claude.com/docs/en/about-claude/pricing",
+  "https://artificialanalysis.ai/models/releases/claude-opus-5",
+]
 
 [models.claude-opus-4-8]
 label = "Opus 4.8"
 status = "dominated"
-reason = "Dominated by Opus 5.5; still used automatically by Opus 5.5 cyber safeguards, nothing to route. Figures to record at the first refresh."
-
-[models.claude-fable-5-1]
-label = "Fable 5.1"
-status = "excluded"
-reason = "Replaced by Opus 5.5."
+reason = "Dominated: index v4.3.2 max 42 at $4.08/task, the index Opus 5.5 low reaches at $0.55 (7.4x cheaper). Opus 5.5's cyber safeguards still fall back to it automatically; nothing to route."
+api_id = "claude-opus-4-8"
+efforts = ["low", "medium", "high", "xhigh", "max"]
+price = { input = 5.0, output = 25.0, cache_read = 0.50, cache_write_5m = 6.25, cache_write_1h = 10.0 }
+verified_at = "2026-09-27"
+sources = [
+  "https://platform.claude.com/docs/en/about-claude/pricing",
+  "https://artificialanalysis.ai/models/releases/claude-opus-4-8",
+]
 
 # ---------------------------------------------------------- measurements
 # Artificial Analysis Intelligence Index v4.3.2 (knowledge work + terminal
 # agentic; not pure Claude Code — the ledger confirms or corrects).
+# Re-read 2026-09-27: AA revised several times per task since 2026-09-26.
 
 [[measurements]]
 model = "claude-opus-5-5"
 effort = "low"
 index = 42
 cost_per_task = 0.55
-time_per_task_s = 80
+time_per_task_s = 81
+output_tokens_per_task = 10_000
 benchmark_version = "v4.3.2"
-measured_at = "2026-09-26"
-source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
 
 [[measurements]]
 model = "claude-opus-5-5"
@@ -112,47 +176,62 @@
 index = 51
 cost_per_task = 1.34
 time_per_task_s = 203
+output_tokens_per_task = 26_000
 benchmark_version = "v4.3.2"
-measured_at = "2026-09-26"
-source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
 
 [[measurements]]
 model = "claude-opus-5-5"
 effort = "high"
 index = 54
 cost_per_task = 1.82
-time_per_task_s = 262
+time_per_task_s = 284
+output_tokens_per_task = 36_000
 benchmark_version = "v4.3.2"
-measured_at = "2026-09-26"
-source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
 
 [[measurements]]
 model = "claude-opus-5-5"
 effort = "xhigh"
 index = 56
 cost_per_task = 3.46
-time_per_task_s = 458
+time_per_task_s = 468
+output_tokens_per_task = 66_000
 benchmark_version = "v4.3.2"
-measured_at = "2026-09-26"
-source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
 
 [[measurements]]
 model = "claude-opus-5-5"
 effort = "max"
 index = 58
 cost_per_task = 5.98
+output_tokens_per_task = 119_000
 benchmark_version = "v4.3.2"
-measured_at = "2026-09-26"
-source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
+
+[[measurements]]
+model = "claude-haiku-4-5"
+# Reasoning variant (extended thinking). Non-reasoning: index 15, no cost published.
+index = 17
+cost_per_task = 0.28
+time_per_task_s = 187
+output_tokens_per_task = 18_000
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-4-5-haiku"
 
 [[measurements]]
 model = "claude-sonnet-5"
 effort = "low"
 index = 24
 cost_per_task = 0.51
-time_per_task_s = 150
+time_per_task_s = 157
 benchmark_version = "v4.3.2"
-measured_at = "2026-09-26"
+measured_at = "2026-09-27"
 source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
 
 [[measurements]]
@@ -160,23 +239,337 @@
 effort = "medium"
 index = 28
 cost_per_task = 1.00
-time_per_task_s = 253
+time_per_task_s = 276
 benchmark_version = "v4.3.2"
-measured_at = "2026-09-26"
+measured_at = "2026-09-27"
 source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
 
-# Sonnet 5 high/xhigh: not recorded (range medium→max 28→38 is dominated).
+[[measurements]]
+model = "claude-sonnet-5"
+effort = "high"
+index = 32
+cost_per_task = 1.79
+time_per_task_s = 404
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
+
+[[measurements]]
+model = "claude-sonnet-5"
+effort = "xhigh"
+index = 34
+cost_per_task = 2.87
+time_per_task_s = 597
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
 
 [[measurements]]
 model = "claude-sonnet-5"
 effort = "max"
 index = 38
 cost_per_task = 5.09
-time_per_task_s = 862
+time_per_task_s = 997
 benchmark_version = "v4.3.2"
-measured_at = "2026-09-26"
+measured_at = "2026-09-27"
 source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-sonnet-5"
 
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "low"
+index = 47
+cost_per_task = 2.37
+time_per_task_s = 245
+output_tokens_per_task = 22_000
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "medium"
+index = 49
+cost_per_task = 2.98
+time_per_task_s = 318
+output_tokens_per_task = 28_000
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "high"
+index = 51
+cost_per_task = 3.91
+time_per_task_s = 445
+output_tokens_per_task = 38_000
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "xhigh"
+index = 53
+cost_per_task = 5.98
+time_per_task_s = 646
+output_tokens_per_task = 61_000
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "max"
+index = 53
+cost_per_task = 7.63
+time_per_task_s = 711
+output_tokens_per_task = 78_000
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/comparisons/claude-opus-5-5-vs-claude-fable-5-1"
+
+[[measurements]]
+model = "claude-fable-5"
+effort = "max"
+index = 50
+cost_per_task = 8.75
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/claude-fable-5"
+
+[[measurements]]
+model = "claude-opus-5"
+effort = "low"
+index = 39
+cost_per_task = 1.10
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/claude-opus-5"
+
+[[measurements]]
+model = "claude-opus-5"
+effort = "medium"
+index = 45
+cost_per_task = 2.19
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/claude-opus-5"
+
+[[measurements]]
+model = "claude-opus-5"
+effort = "high"
+index = 48
+cost_per_task = 3.61
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/claude-opus-5"
+
+[[measurements]]
+model = "claude-opus-5"
+effort = "xhigh"
+index = 50
+cost_per_task = 4.88
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/claude-opus-5"
+
+[[measurements]]
+model = "claude-opus-5"
+effort = "max"
+index = 51
+cost_per_task = 5.86
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/claude-opus-5"
+
+[[measurements]]
+model = "claude-opus-4-8"
+effort = "max"
+index = 42
+cost_per_task = 4.08
+benchmark_version = "v4.3.2"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/models/releases/claude-opus-4-8"
+
+# CursorBench 4.0 (2026-09-10): agentic coding on real Cursor sessions, run in
+# Cursor's harness; index = score in %, cost per task at list prices with
+# cache reads/writes. Never compared with v4.3.2 (frontier.py ignores it by
+# default; see it with --version cursorbench-4.0). Opus 5.5 costs are
+# Anthropic's estimates from Cursor's token counts; the others are Cursor's.
+
+[[measurements]]
+model = "claude-opus-5-5"
+effort = "low"
+index = 43.7
+cost_per_task = 1.17
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-opus-5-5"
+effort = "medium"
+index = 52.5
+cost_per_task = 2.91
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-opus-5-5"
+effort = "high"
+index = 56.0
+cost_per_task = 3.97
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-opus-5-5"
+effort = "xhigh"
+index = 56.0
+cost_per_task = 6.98
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-opus-5-5"
+effort = "max"
+index = 57.8
+cost_per_task = 13.43
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-sonnet-5"
+effort = "low"
+index = 24.1
+cost_per_task = 1.39
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-sonnet-5"
+effort = "medium"
+index = 28.0
+cost_per_task = 2.31
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-sonnet-5"
+effort = "high"
+index = 30.8
+cost_per_task = 3.48
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-sonnet-5"
+effort = "xhigh"
+index = 32.0
+cost_per_task = 4.55
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-sonnet-5"
+effort = "max"
+index = 34.1
+cost_per_task = 7.17
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "low"
+index = 45.1
+cost_per_task = 5.44
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "medium"
+index = 46.8
+cost_per_task = 7.05
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "high"
+index = 49.2
+cost_per_task = 9.08
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "xhigh"
+index = 51.6
+cost_per_task = 13.01
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "max"
+index = 51.8
+cost_per_task = 17.28
+benchmark_version = "cursorbench-4.0"
+measured_at = "2026-09-27"
+source = "https://cursor.com/cursorbench"
+
+# Artificial Analysis Coding Agent Index v1.5 (DeepSWE v1.1, Terminal-Bench
+# 4.0, SWE-Atlas-QnA), run in Claude Code, max effort only; data materialized
+# 2026-09-26. Time = agent wall time per task. Never compared with v4.3.2.
+
+[[measurements]]
+model = "claude-opus-5-5"
+effort = "max"
+index = 66.0
+cost_per_task = 13.04
+time_per_task_s = 3867
+output_tokens_per_task = 333_177
+benchmark_version = "aa-coding-agent-index-v1.5"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/agents/coding-agents"
+
+[[measurements]]
+model = "claude-fable-5-1"
+effort = "max"
+index = 62.2
+cost_per_task = 12.39
+time_per_task_s = 2090
+output_tokens_per_task = 133_632
+benchmark_version = "aa-coding-agent-index-v1.5"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/agents/coding-agents"
+
+[[measurements]]
+model = "claude-opus-5"
+effort = "max"
+index = 59.7
+cost_per_task = 10.79
+time_per_task_s = 2516
+output_tokens_per_task = 136_656
+benchmark_version = "aa-coding-agent-index-v1.5"
+measured_at = "2026-09-27"
+source = "https://artificialanalysis.ai/agents/coding-agents"
+
 # ------------------------------------------------------------ main tiers
 # The main session stays on one model; only effort varies (per-turn effort
 # keeps the prompt cache across effort changes). Tier criteria are the levels
@@ -231,7 +624,7 @@
 [tiers.subagent.haiku]
 rank = 1
 model = "claude-haiku-4-5"
-cost = 0.15  # no measurement: Opus 5.5 low x the Haiku/Opus price ratio
+# cost comes from its index v4.3.2 measurement ($0.28/task; was 0.15 by price ratio)
 criteria = "Mechanical, fully specified work: finding files, listing or extracting facts, grepping, applying an explicit edit, one-line answers."
 
 [tiers.subagent.opus-low]
```
