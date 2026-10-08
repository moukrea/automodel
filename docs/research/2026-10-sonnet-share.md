# Opus versus Sonnet after the model mix (2026-10-08)

The owner still felt that automodel runs Opus too often where Sonnet would
do, after v0.24.1 moved most subagent levels to Sonnet 5.5. This note checks
where the Opus spend comes from and tests two changes. Neither measured
better, so neither shipped.

## Where the Opus spend comes from

The figures come from the ledger, on Home and Work, at list prices. Cache
writes are priced at the 1-hour rate.

- **Home, 12:00 to 18:00:**
  - Total spend was $595.
  - Opus subagents took 73% of it.
  - Most of that came from agents spawned this morning on the retired
    `opus-high` tier. Long-lived agents that are talked to again keep the
    model they were spawned with, so this share fades on its own.
- **Agents spawned after 13:00 ($126):**
  - Sonnet subagents took about 30%.
  - One `opus-xhigh` agent took 52% ($65). Jev answered sonnet-xhigh 0.49 and
    opus-xhigh 0.41, and the cost-aware policy picked Opus.
  - Forks and continued agents inheriting the parent's Opus tier took about
    19%.
- **Decisions since the mix:** 16 of the 18 new subagent decisions went to
  Sonnet. The one above is the only case where the policy flipped a Sonnet
  answer to Opus.
- **Work, since 2026-10-01 ($535):** the main thread took 68% of the spend,
  all of it on Opus by design.

## 1. A lower underprovision penalty for subagents

The test replays the saved Jev answers on all 74 subagent eval cases, each
asked 3 times, using `--answers`. Only the penalty changes between rows.

| Penalty | Train exact | Train acceptable | Test exact | Test acceptable | Opus decisions (train / test) |
|---|---:|---:|---:|---:|---:|
| 0.5 | 75.0% | 93.8% | 84.6% | 96.2% | 24/144 · 15/78 |
| 0.75 | 77.1% | 95.8% | 84.6% | 96.2% | 24/144 · 15/78 |
| 1.0 | 81.9% | 97.9% | 84.6% | 96.2% | 24/144 · 15/78 |
| 1.25 | 83.3% | 97.9% | 88.5% | 100% | 24/144 · 15/78 |
| **1.5 (current)** | **84.7%** | **99.3%** | **89.7%** | **100%** | 24/144 · 15/78 |

- **Opus share:** the number of Opus decisions doesn't move at any penalty.
  In these cases, Opus is chosen only when Jev is clearly on Opus.
- **Accuracy:** a lower penalty only moves decisions down among the Sonnet
  levels, and accuracy drops with it.
- **Real flips:** the close Sonnet/Opus call happened once in 18 real
  decisions.
- **Verdict:** no evidence for a scope-specific or model-aware penalty.
  Kept at 1.5.

## 2. A main session on Sonnet from its start

The 2026-10 model-mix study ruled out switching the main model for each
stretch of work, because each switch rewrites the whole context. This test
covers the variant without that per-stretch switching:

- A session whose first decision is at or below level T runs on Sonnet.
- It moves to Opus for good at its first decision above T. That move writes
  the whole context once, at $8/M, less the $0.20/M read it replaces.
- Sonnet is priced from the same tokens at half the rate, with three step
  multipliers:
  - **equal:** the same input and output;
  - **mid:** 1.2× input and 1.5× output;
  - **pess:** 1.45× input and 2.5× output.
- High maps to Sonnet xhigh, as in the subagent tiers.

| T | Home ($2,283 of routed main, since 09-25) | Work ($349) |
|---|---|---|
| ≤ low | −0.3% / −0.1% / 0.0% | −1.5% / −1.1% / −0.6% |
| ≤ medium | −0.8% / −0.6% / −0.2% | −2.5% / −1.7% / −0.7% |
| ≤ high | −7.1% / −5.2% / −2.5% | −26.7% / −19.9% / −10.6% |

Each cell gives equal / mid / pess.

- **Sessions that stay low or medium are rare.** At T ≤ medium, Home saves
  under 1% and Work under 2.5%.
- **T ≤ high saves more, but it is small next to the total.** It is 2.5–7%
  of Home's routed main spend, which is itself about 10% of Home's spend.
  Overall that is under 1% of weekly usage.
- **T ≤ high has a quality cost.** It runs the main thread on Sonnet xhigh
  in place of Opus high, 1.7–2.9 points under it on four of five per-effort
  coding benchmarks. The main thread is the one the owner talks to. That
  quality risk is not worth under 1% of usage.
- **Verdict:** no Sonnet main tier.

## Noted for follow-up

- **Spend that inherits the parent's tier:** $100 of $506 of subagent spend
  since noon. It comes from agents billed under the parent's main tier,
  mostly an agent continued for days that keeps running on Opus xhigh.
- **Whether that inheritance should be routed:** this is a separate question
  from the two above. Forks must keep the parent's model for its cache. A
  continued named agent has its own context, so it could get its own
  subagent decision.
