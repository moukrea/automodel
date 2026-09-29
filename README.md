<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/media/wordmark-dark.png">
    <img src="docs/media/wordmark-light.png" alt="automodel" width="440">
  </picture>
</h1>

A small tool for Claude Code: pick **Jev (auto)** in `/model`, and it picks
the model and effort for each prompt, subagent and workflow step. A quick
question gets Haiku or low effort, a race condition extra-high; subagents get
their own pick (Haiku for a file listing, Sonnet 5.5 for a summary).
Decisions come from [TypeSafe Jev](https://docs.typesafe.ai) on OpenRouter
(under a second, ~$0.00004 each); your Claude traffic stays on your own
subscription.

<p align="center">
  <a href="https://youtu.be/XOAAOmwHQSw" title="automodel overview (2 min, YouTube)"><img src="docs/media/thumb-overview.jpg" width="400" alt="automodel overview, 2 minutes"></a>
  <a href="https://youtu.be/KeMISZr58YE" title="automodel full tour (13 min, YouTube)"><img src="docs/media/thumb-tour.jpg" width="400" alt="automodel full tour, 13 minutes"></a>
  <br><sub><a href="https://youtu.be/XOAAOmwHQSw">Overview (2 min)</a> · <a href="https://youtu.be/KeMISZr58YE">Full tour (13 min)</a> · <a href="https://moukrea.github.io/automodel/">Documentation site</a></sub>
</p>

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/moukrea/automodel/main/install.sh | sh
```

Linux or macOS (Windows: [below](#windows)). The script installs the binary in
`~/.local/bin`, starts the local proxy, wires Claude Code (your
`settings.json` is backed up first) and asks for your OpenRouter key. Then
open `/model` in Claude Code and pick **Jev (auto)**. `automodel doctor`
checks the whole setup, one line per check with the fix for each problem.

The proxy runs as a systemd user service, or a launchd agent on macOS.
Without a systemd user session (containers, WSL1, minimal distros) it runs as
a background process that is not restarted after a reboot: `install` prints
the line to add to `~/.profile` (`automodel start` does nothing when the
proxy already runs).

- Updates: the service installs new releases by itself (checked daily,
  applied when idle). `automodel update` does it now; `[update] auto = false`
  in the config turns it off.
- Uninstall: `automodel uninstall` (config and state are kept).
- From source: `go build -o ~/.local/bin/automodel ./cmd/automodel && automodel install`.

### Windows

In PowerShell (no admin rights needed):

```powershell
irm https://raw.githubusercontent.com/moukrea/automodel/main/install.ps1 | iex
```

The binary goes to `%LOCALAPPDATA%\automodel\bin` (added to your user
PATH). The proxy runs as a background process and starts at each logon
through `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` (a console
window flashes briefly at logon). Claude Code reads
`%USERPROFILE%\.claude\settings.json` and runs the hook and statusline
commands with Git Bash, or PowerShell without Git for Windows: automodel
writes them for both. Config and state live under `%USERPROFILE%\.config`
and `%USERPROFILE%\.local\state`, as on Linux. Self-updates work the same;
the replaced binary is kept as `automodel.exe.old` until the proxy restarts.

## How it works

One Go binary does everything: the proxy, the hooks, the statusline, the
report and the catalog checks.

```
UserPromptSubmit ─▶ automodel hook decide ─▶ Jev ─▶ state/sessions/<id>.json ─┐
PreToolUse[Agent] ─▶ automodel hook agent  ─▶ Jev ─▶ model alias + pending effort┤
PreToolUse[Workflow] ─▶ automodel hook workflow ─▶ {model, effort} per agent()  │
PreCompact / SessionStart / PostModelSwitch ─▶ markers                          │
Claude Code ─(model "jev")─▶ automodel serve ─(model + effort rewritten)─▶ api.anthropic.com
statusLine ─▶ automodel statusline:  jev → opus-5.5·xhigh +ultracode 0.82  ↻ switched
```

`install` does everything and is idempotent:

- writes `~/.config/automodel/config.toml` (0600) and the catalog shipped in
  the binary next to it (updated with new releases unless you edit it),
  keeping your existing
  statusline as `statusline_command` (rendered before the automodel segment;
  it runs detached behind a cache, so a slow script never delays the
  segment: Claude Code cancels a statusline run whenever the next update
  arrives), except [agentline](#status-line), which shows the segment
  itself and is left in place;
- installs and starts the service (systemd user unit `automodel.service`, a
  launchd agent on macOS, or else a background process with a pidfile and
  `proxy.log` in the state dir), then checks
  the proxy is listening **before** touching settings. **If the proxy is down,
  Claude Code no longer works** while `ANTHROPIC_BASE_URL` points at it;
- backs up `~/.claude/settings.json` (`settings.json.automodel-backup-*`) and
  merges the `env` vars, the hooks, the statusline and
  `permissions.allow: ["Workflow"]`.
- adds two user slash commands, `~/.claude/commands/why.md` and `flag.md`:
  `/why` shows the current session's latest decisions, `/flag xhigh too hard
  for low` flags the last one (a file of that name you wrote yourself is
  kept).

The key lives in the config file (read on every hook call, no restart
needed); `automodel key set` reads it on stdin:

```toml
openrouter_api_key = "sk-or-..."
```

`$OPENROUTER_API_KEY` overrides it if set. Without a key, every decision falls
back to the default tier, and the statusline says why
(`⚠ fallback ⚠ jev: no OpenRouter key`; also shown for a rejected key,
missing credits, rate limits and timeouts).

If the proxy stops, the `SessionStart` and `UserPromptSubmit` hooks restart it
once; if it still doesn't answer, the prompt is blocked with a message saying
how to fix it, instead of a connection error on every request.

Only sessions on `jev` are routed: the hooks, the statusline segment and the
proxy leave sessions on a named model (`opus`, `opus[1m]`, …) untouched. The
installer also sets `_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1`, since the
proxy forwards to api.anthropic.com unchanged. Otherwise the custom
`ANTHROPIC_BASE_URL` makes Claude Code treat every session as a gateway
session, and Opus drops from 1M to a 200K window.
Routed `jev` sessions get the full window too (Opus 5.5 has a native 1M
window; the proxy only sends the long-context beta to models that need it),
and main tiers must offer at least
`meta.main_min_context` (1M).

`automodel install --dry-run` only prints what would be merged.
`automodel uninstall` removes the settings entries and the slash commands,
restores your statusline (agentline's is left as is) and removes the service
(config and state are kept).

## Status line

On a `jev` session the automodel segment shows what the routing chose:
`jev → opus-5.5·xhigh +ultracode 0.86` (model, effort, mode, Jev's
confidence), `(default)` before the first decision, `(pinned)` while your
`/effort` wins, `· real effort: low (Claude Code shows xhigh)` when Claude
Code's own spinner shows another effort than the one routed, `⚠ fallback` and
`⚠ jev: <why>` when Jev couldn't be asked, `⚠ budget` over the spending cap,
and `↻ switched|compact|cold` for `statusline_flash` (30s) after a
redecision. Sessions on a named model show nothing.

**Your own status line** is kept: `install` saves it as `statusline_command`
in `config.toml` and prints it above the segment (it runs detached behind a
cache and sees `AUTOMODEL_CHAINED=1` in its environment). Set or change
`statusline_command` there to chain another one.

**[agentline](https://github.com/moukrea/agentline)** renders the routing
natively instead: the routed model and effort in place of Claude Code's
`Jev (auto)`, then the confidence and flags in its own `route` segment, fitted
to the terminal width. A status line whose command contains `agentline` is
left in place by `install`, the daily self-update and `uninstall` (never
chained), and `doctor` reports `agentline shows the automodel segment`.

**Other status line authors**: `automodel [--config path] statusline --json`
reads Claude Code's status line JSON on stdin, records model switches like
the text status line, never runs `statusline_command`, and prints one line:

```json
{"v":1,"routed":true,"alias":"jev","model":"claude-opus-5-5","label":"Opus 5.5","effort":"xhigh","mode":"ultracode","state":"routed","confidence":0.86,"pin":"","issue":"","flash":"","budget":"","claude_effort":"","text":"jev → opus-5.5·xhigh +ultracode 0.86"}
```

`{"v":1,"routed":false}` for a session automodel doesn't route (with every
field and `state` `error` when the catalog can't load: read `routed` first).
`state` is `routed|default|fallback|pinned|error` (`error`: the catalog can't
load, `issue` says `catalog`); `confidence` is 0 unless `routed`; `pin` is the
pinned effort; `issue` why Jev couldn't be asked; `flash`
`switched|compact|cold|""`; `budget` `over` past the spending cap, else
`""`; `claude_effort` the effort Claude Code itself shows when it differs from
`effort` (its spinner shows its own setting, not the routed one), else `""`;
`text` the text segment. Find the command in
`settings.json` (the `UserPromptSubmit` hook ending in ` hook decide`, minus
that suffix) and check `automodel help` mentions `statusline … --json`
before calling it: older releases would render (and chain) the text status
line instead. Skip the call when `AUTOMODEL_CHAINED=1`.

## When it decides (main session)

| Trigger | Condition | Cost of switching |
|---|---|---|
| `initial` | first prompt of the session (including after `/clear`) | none |
| `compact` | after a compaction, using the summary | none (the cache is rebuilt anyway) |
| `cold` | inactive for longer than `cache_ttl`, or resumed with an expired cache | none for a top-level change |
| `warm` | any other prompt (`features.warm_decisions`) | see below |

**Warm turns.** Changing the model rebuilds the whole prompt cache, and so
does changing the top-level effort. Models with `per_turn_effort` (Opus 5.5)
accept an effort-only system message mid-conversation: the proxy keeps the
top-level effort fixed for the whole conversation and inserts those messages
at the same places on every request, so an effort change keeps the cache
(measured: 41k tokens read, 51 written, instead of 13k rewritten). A warm
switch must:

1. **pay back**: with `features.cost_aware`, the expected cost of the work at
   each tier (catalog cost per task; working *below* the right tier costs
   `underprovision_penalty` × the gap, working above it costs the gap once)
   plus the switch cost (0 for a per-turn effort change, else the context
   written to the cache again) picks the tier. When no answer could pay back a
   switch, Jev is not even asked;
2. when the switch costs something (a cache rebuild; not a per-turn effort
   change), **be confident**: Jev's confidence ≥
   `features.warm_min_confidence`;
3. and, for such a switch, **not downgrade work in progress**: when Jev says
   the prompt continues the ongoing work, effort can go up, never down.

The confidence gate only holds downgrades: an upgrade under doubt is the safe
side. A prompt that **only informs** the work in progress (a fact, a
preference, a correction, an answer to Claude's question: "FYI it only
happens with 8 workers", "env vars win") keeps the decision in force, even
where a switch would be free (`meta.informs_threshold`, 0.6).

**Haiku in the main session** is an *asked* tier: Jev answers "could a small,
fast model handle this as well?" in the same call, and a clear yes (0.92)
turns `low` into Haiku. It is only taken when there is no conversation cache
to lose (a new session, after /compact or a pause) or when the session is
already on it; a warm Opus session never moves there for a side question (a
Haiku turn on a warm cache costs about Opus low, and coming back rebuilds the
context). Past `max_context` (150K of Haiku's 200K) the session moves to
Opus, within the turn if needed.

A free switch (per-turn effort) follows Jev's answer: holding the tier there
made sessions sticky (`docs/research/2026-09-routing-quality.md`). A bare
go-ahead ("yes", "continue") keeps the tier without asking Jev, also after a
compaction or a pause, unless it answers a proposal ("Want me to fix it?"):
then it starts that work and is routed.

**What Jev is asked** (one call): a *Score* over the scope's tiers (they are
ordered, and a Score sharpens the distribution: mean confidence 0.92 vs 0.88
for a Choice on `testdata/eval`), a yes/no *Noul* per mode, and on warm turns
a Noul on whether the prompt continues the work in progress and, in the main
session, one on whether it only informs that work; plus a Noul per asked
tier (Haiku). The state
carries the prompt, the compaction summary, recent prompts, the last
assistant reply, the tier in force and the session size (context, peak,
compactions).

With `features.cost_aware` off, the v1 confidence rule applies: `≥ θ_act` →
Jev's choice; below it → the higher of the top two; `< θ_low` → one more rank
up. Per-repo floor and ceiling via `.automodel.toml`: `min_tier`, `max_tier`,
`min_subagent_tier`, `max_subagent_tier`. All `[features]` off gives the v1
router.

**Ultracode** is a *mode*, not a model: parallel multi-agent orchestration
layered on the chosen tier, whatever its model, with effort raised to
`xhigh`. Jev answers it as a separate yes/no question (threshold 0.75, from
the eval); the hook injects the standing opt-in to workflow orchestration
(repeated after each compaction, with an "off" notice when it ends).

**Subagents**: the Agent tool only accepts an alias (`opus`, `haiku`…) and has
no effort field. The hook sets the alias, and the proxy binds the effort to
the subagent (`X-Claude-Code-Agent-Id`) on its first request. **Workflows**:
each `agent()` call site that sets no `model`/`effort` gets its own tier,
injected as `{model, effort, ...(opts)}`, so the script's explicit options
still win.

## Your choice wins

- **`/effort`** in Claude Code pins that effort for the session: routing
  pauses (statusline `(pinned)`) until you set `/effort` back to its default.
- **`[effort:xhigh]`** (or `low`, `medium`, `high`, `max`) anywhere in a
  prompt pins it the same way; **`[effort:auto]`** hands control back to Jev.
- **`[model:sonnet]`** (any catalog model by alias, with a 1M window) pins
  the model too, at the effort of `[effort:X]` or the current one;
  `[model:auto]` releases it. Switching model rewrites the prompt cache.
  (`/model opus` in Claude Code leaves automodel out of the session
  entirely.)
- **"think harder"**, "ultrathink", "take your time", "réfléchis bien"…:
  at least one tier above the current one for that prompt.
- A bare **go-ahead** ("yes", "continue", "vas-y", "lgtm") keeps the current
  tier without asking Jev (`features.fast_path`).
- A turn you **interrupted** (Esc) is passed to Jev as a signal.

Per repository, `.automodel.toml` at the repo root:

```toml
min_tier = "high"               # floor / ceiling for the main session
max_tier = "xhigh"
min_subagent_tier = "opus-low"  # same for subagents
disable_modes = ["ultracode"]
privacy = "metadata"            # a repo can make privacy stricter, never looser
```

## Spending cap

```toml
[budget]
usd_per_day = 20               # 0 = off (the default)
usd_per_session = 0            # optional, per session
max_tier_when_over = "medium"  # the highest tier once a cap is reached
max_subagent_tier_when_over = "opus-medium"
```

The proxy prices every response with the catalog and keeps the day's total
(`state_dir/spend.json`, local day) and each session's. Once a cap is
reached, routing picks nothing above `max_tier_when_over` until the next
day (or session); the status line shows `⚠ budget`, and `why` and `report`
show the cap and the decisions it lowered. Your pins are not capped: it's
your call.

## What leaves your machine

- **Claude traffic** goes to api.anthropic.com through the local proxy,
  unchanged except for the model, the effort, `max_tokens` and effort-only
  system messages.
- **Each decision** sends a small routing state to Jev on OpenRouter
  (~$0.00004 per call): the prompt (truncated), your last few prompts, the
  start of the last reply, the compaction summary after a `/compact`, the
  session size, and repo signals (languages, file count, diff stat, the
  start of `CLAUDE.md`).
- With **`privacy = "metadata"`** (in the config, or per repo) no text is
  sent: only sizes and task-kind hints (bug, concurrency, security, review…),
  the phase, the tier in force and the repo languages. Decisions get less
  precise.
- Otherwise only the update check (GitHub releases API, daily unless
  `[update] auto = false`), and `automodel doctor`'s key check against
  OpenRouter. No telemetry: the ledger (`~/.local/state/automodel/`) stays
  local.

## Tuning: automodel's defaults, or yours

Everything that drives a decision lives in the **catalog**: models and prices,
the tiers and the criteria Jev reads for each, the modes (ultracode), the
questions Jev answers and their thresholds, the policy (how much worse
under-provisioning is than over-provisioning), and how much of the
conversation Jev sees. There is no model ID in the code.

- **Default tuning** (`tuning = "default"`, the default): automodel's catalog,
  shipped in the binary, measured on the labeled cases before each release and
  updated with every release. Nothing to maintain.
- **Custom tuning** (`tuning = "custom"`): your file (`catalog` in the config,
  `~/.config/automodel/catalog.toml` by default). A partial file, with only
  the keys you change, is layered over the default: you keep automodel's
  improvements for the rest. A whole catalog (`automodel tuning init --full`)
  is used as it is and no longer follows automodel's updates. A broken file
  never stops routing: the default takes over, and `automodel doctor` says so.

```sh
automodel tuning                    # which tuning routes, and what your file changes
automodel tuning init [--full]      # create your file: a template of the usual knobs, or a whole copy
automodel tuning diff               # your changes, next to automodel's defaults
automodel tuning show [--default]   # the catalog routing uses (or the default one)
automodel tuning use custom         # route with your file; `use default` goes back
automodel eval --catalog <file>     # measure a file (partial or whole) on the labeled cases
```

A partial file looks like this:

```toml
[meta]
underprovision_penalty = 2.0          # lean higher when unsure

[tiers.main.medium]
criteria = "A routine change with a clear recipe in one area, including our Terraform modules."

[tiers.main.haiku]
threshold = 0.95                      # Haiku only when Jev is really sure

[questions.informs]
question = "Does the new prompt only add information for the work in progress?"
yes = "..."
no = "..."

[state]
recent_prompts = 3                    # show Jev fewer past prompts
```

A partial file's tables merge key by key; its arrays (such as
`[[measurements]]`) replace the default's whole. Behaviour switches (warm decisions, the spending cap, privacy) stay in
the config file.

**Refreshing it**: the `refresh-model-catalog` skill (`.claude/skills/`)
researches models, prices and benchmarks, proposes a catalog with evidence
(`scripts/frontier.py`, `automodel eval`) and applies it only with your
approval. Run from the automodel repository it updates the default tuning
(logged in `catalog-history.md`); run anywhere else it writes *your* custom
file and switches you to it.

```sh
automodel catalog check [--json]    # validation of the catalog in use (non-zero exit on error, for CI)
.claude/skills/refresh-model-catalog/scripts/frontier.py catalog.toml [--json]
```

## Measurement

```sh
automodel why [--session id] [-n 5] [--scope main] [--follow]  # what Jev answered for the last decisions, and why
automodel report [--since 7d] [--json] [--baseline xhigh]
automodel eval [--catalog path] [--format score|choice]    # Jev on labeled cases: accuracy, confidence, calibration
automodel flag [--session id] [--n 1] --want xhigh [--note "..."]  # that pick was wrong
```

`flag` turns a decision (default: the session's latest main decision) into
a labeled case in `~/.local/state/automodel/flagged.jsonl`, with the routing
state that was sent to Jev, the tier you wanted and Jev's probabilities;
`automodel eval --cases ~/.local/state/automodel/flagged.jsonl` replays
them. The states are kept locally per decision (`state_dir/states`, bounded
to 2 MB per session, metadata only under `privacy = "metadata"`);
`record_states = false` turns this off.

`why` shows each decision like the demo's popup: every level with its
probability, the pick, the previous tier, and the reasons (continuation,
switch cost and expected gain, pin, go-ahead, your signals, Jev failures).
`report` lists **suggestions** drawn from your habits, each with its
evidence, only after 5 events or more: a repo where you often pin an effort
above Jev's pick (or ask to think harder, or interrupt turns picked below
`high`) gets a `min_tier` for its `.automodel.toml`; several flagged cases
wanting the same tier suggest reviewing its catalog criteria. It ends
with a conservative estimate of the savings against running everything
at `--baseline`: only the output (response and thinking) is
scaled by the catalog's cost ratios; input and cache reads count as they
were.

Tier mix, modes, warm turns (evaluated, switched, kept and why, skipped),
fallback rate, confidence, cache hit rate for routed vs unrouted
sessions, Jev cost, tokens per tier, and shadow-mode agreement (when
`jev_shadow_model` is set). Raw data: `~/.local/state/automodel/ledger.jsonl`;
hook logs: `hooks.log` next to it.

## Known limits

- Claude Code's spinner shows **its own** effort setting ("thinking with
  xhigh effort"), not the routed one. The statusline is the source of truth.
- With any custom `ANTHROPIC_BASE_URL` (a proxy), Claude Code turns off its
  server-side message threads, which it only uses when talking to
  api.anthropic.com directly. The first prompts of a session reuse a bit less
  of the prompt cache; over real routed sessions the cache still served
  98% of the input tokens.
- A pin (`/effort`, `[effort:X]`) applies to the session's current model.
  Claude Code tells the proxy about `/effort` only with the next request,
  so the first prompt after it is still routed; the pin applies from that
  prompt's first request.

## Tests

```sh
go test ./...
```

The tests cover validation (with a Go ↔ `frontier.py` cross-check), the
policy, the hooks (fake Jev), the proxy (fake upstream: rewrite, SSE,
subagent binding, count_tokens, Haiku stripping), the report, the statusline
and the transcript reader. Spike results are in `docs/spikes.md`.
