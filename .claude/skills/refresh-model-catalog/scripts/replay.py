#!/usr/bin/env python3
"""Replay the owner's real traffic (the router ledger) under other model mixes.

Prices every recorded request again, for subagents and for routed main
sessions, as if another (model, effort) had done the same work: the agent
takes `s` times the steps and `o` times the output tokens (benchmark
multipliers), its context grows with the extra steps, and Haiku 5.5 pays its
5x rate on every request whose prompt passes 100k tokens. Main sessions also
pay the cache rebuild of each model switch. Read-only: nothing is written.

  replay.py                         # subagent mixes, central multipliers
  replay.py --basis pessimistic     # Sonnet takes 1.37x CursorBench's relative steps
  replay.py --main                  # Sonnet main tiers, with switch costs
  replay.py --configs               # owner cost of every config, Opus high = 1

Multipliers: CursorBench 4.0 steps and tokens per task (every model and
effort; read 2026-10-08). Context growth: total input ~ steps^1.3 (AA Coding
Agent Index v1.5, Haiku and Sonnet rows). Method and results:
docs/research/2026-10-model-mix.md. Stdlib only (Python 3.11+).
"""
from __future__ import annotations

import argparse
import collections
import datetime as dt
import glob
import json
import math
import os

LEDGER = os.path.expanduser("~/.local/state/automodel/ledger.jsonl")
PROJECTS = os.path.expanduser("~/.claude/projects")
EFF = ["low", "medium", "high", "xhigh", "max"]
ALPHA = 1.3
# $/MTok: input, output, cache write 5m, cache write 1h, cache read (2026-10-08).
PRICES = {
    "claude-fable-5-1": (10, 50, 12.5, 20, 0.25),
    "claude-opus-5-5": (4, 20, 5, 8, 0.20),
    "claude-sonnet-5-5": (2, 10, 2.5, 4, 0.10),
    "claude-haiku-5-5": (0.10, 0.50, 0.125, 0.20, 0.01),
    "claude-haiku-5-5-long": (0.50, 2.50, 0.625, 1.0, 0.05),  # whole request, prompt > 100k
    "claude-haiku-4-5-20251001": (1, 5, 1.25, 2, 0.10),
}
MODEL = {"opus": "claude-opus-5-5", "sonnet": "claude-sonnet-5-5", "haiku": "claude-haiku-5-5", "fable": "claude-fable-5-1"}
FAM = {v: k for k, v in MODEL.items()}
# CursorBench 4.0 steps and tokens per task, low..max.
STEPS = {"opus": [28, 54, 68, 109, 185], "sonnet": [18, 22, 41, 78, 170], "haiku": [25, 39, 59, 93, 163], "fable": [51, 63, 77, 101, 128]}
OUT = {"opus": [15811, 37954, 53078, 101083, 218363], "sonnet": [11668, 16036, 37391, 100158, 271920],
       "haiku": [23382, 42659, 77057, 143813, 325934], "fable": [34795, 45411, 58438, 87294, 117236]}
# Subagent tiers before 2026-10-08 (the ledger's labels) and the levels they were.
LEVEL_CFG = {"haiku": ("haiku", "medium"), "sonnet-low": ("sonnet", "low"), "opus-low": ("opus", "low"),
             "sonnet-high": ("sonnet", "high"), "opus-medium": ("opus", "medium"), "opus-high": ("opus", "high"),
             "sonnet-xhigh": ("sonnet", "xhigh"), "opus-xhigh": ("opus", "xhigh"), "opus-max": ("opus", "max")}
MAIN_TIERS = {"low", "medium", "high", "xhigh", "max"}
MIXES = {
    "current (2026-10-07)": {},
    "Opus low/medium out": {"opus-low": ("sonnet", "high"), "opus-medium": ("sonnet", "xhigh")},
    "+ Opus high -> Sonnet xhigh (2026-10-08)": {"opus-low": ("sonnet", "high"), "opus-medium": ("sonnet", "xhigh"), "opus-high": ("sonnet", "xhigh")},
    "+ Opus xhigh -> Sonnet xhigh": {"opus-low": ("sonnet", "high"), "opus-medium": ("sonnet", "xhigh"), "opus-high": ("sonnet", "xhigh"), "opus-xhigh": ("sonnet", "xhigh")},
    "Opus high -> Opus medium": {"opus-high": ("opus", "medium")},
}


def mult(frm, to, basis):
    s = STEPS[to[0]][EFF.index(to[1])] / STEPS[frm[0]][EFF.index(frm[1])]
    o = OUT[to[0]][EFF.index(to[1])] / OUT[frm[0]][EFF.index(frm[1])]
    if basis == "pessimistic":  # Coding Agent Index max: Sonnet 1.72x Opus's steps, CursorBench 0.92x
        k = 1.37
        if to[0] == "sonnet" and frm[0] != "sonnet":
            s, o = s * k, o * k
        if frm[0] == "sonnet" and to[0] != "sonnet":
            s, o = s / k, o / k
    return s, o


def prompt(x):
    return x["input_tokens"] + x["cache_read_input_tokens"] + x["cache_creation_input_tokens"]


def req_price(model, i, o, r, w, ttl="1h"):
    if model == "claude-haiku-5-5" and i + r + w > 100_000:
        model = "claude-haiku-5-5-long"
    p = PRICES[model]
    return (i * p[0] + o * p[1] + w * (p[3] if ttl == "1h" else p[2]) + r * p[4]) / 1e6


def replay(traj, model, s=1.0, o=1.0, rewrites="1h"):
    """The cost of the work of `traj` (the requests of one agent or stretch) on `model`."""
    P = [prompt(x) for x in traj]
    n, P0 = len(P), P[0]
    n2 = max(1, round(s * n))
    g = (n2 / n) ** (ALPHA - 1) if n > 1 else 1.0
    out = sum(x["output_tokens"] for x in traj) * o / n2
    tot, prev = 0.0, None
    for j in range(n2):
        x = j * (n - 1) / (n2 - 1) if n2 > 1 else 0.0
        k, f = int(math.floor(x)), x - math.floor(x)
        px = P[k] if k + 1 >= n else P[k] * (1 - f) + P[k + 1] * f
        pj = P0 + (px - P0) * g
        if j == 0:
            i, r, w = traj[0]["input_tokens"], traj[0]["cache_read_input_tokens"], traj[0]["cache_creation_input_tokens"]
        else:
            w = max(0.0, pj - prev)
            i, r = 0, pj - w
        prev = pj
        tot += req_price(model, i, out, r, w)
    # Cache rewrites of the real run are wall-clock events (same count on any
    # model), scaled with the context. rewrites="1h": only those a 1-hour TTL
    # keeps (gap under 5 min or over 1 h); "all"; "none".
    if rewrites != "none":
        for k in range(1, n):
            rw = traj[k]["cache_creation_input_tokens"] - max(0, P[k] - P[k - 1])
            if rw <= 0:
                continue
            gap = (traj[k]["_ts"] - traj[k - 1]["_ts"]).total_seconds()
            if rewrites == "1h" and 300 <= gap <= 3600:
                continue
            pk = P0 + (P[k] - P0) * g
            p = PRICES["claude-haiku-5-5-long" if model == "claude-haiku-5-5" and pk > 100_000 else model]
            tot += rw * g * (p[3] - p[4]) / 1e6
    return tot


def load(days, end):
    start = end - dt.timedelta(days=days)
    rows, decisions = [], []
    with open(LEDGER) as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            ts = dt.datetime.fromisoformat(d["ts"])
            if d.get("kind") == "decision" and d.get("trigger") == "workflow":
                d["_ts"] = ts
                decisions.append(d)
            if d.get("kind") != "usage" or d.get("status") != 200 or not start <= ts < end:
                continue
            d["_ts"] = ts
            rows.append(d)
    return rows, decisions


def workflow_levels(decisions):
    """Workflow agents show no tier in the ledger (the script carries model and
    effort): match each to its stage decision by label or phase."""
    started = {}
    for f in glob.glob(PROJECTS + "/*/*/subagents/workflows/*/journal.jsonl"):
        for line in open(f, errors="ignore"):
            try:
                d = json.loads(line)
            except ValueError:
                continue
            if d.get("type") == "started" and d.get("agentId"):
                started[d["agentId"]] = ((d.get("label") or "").lower(), (d.get("phase") or "").lower())
    by = collections.defaultdict(list)
    for d in decisions:
        by[d["session_id"]].append(d)

    def level(session, agent, t0):
        if agent not in started:
            return None
        lab, ph = started[agent]
        best = None
        for d in by.get(session, []):
            dl = (d.get("label") or "").lower()
            if dl and d["_ts"] <= t0 and (dl in (lab, ph) or (len(dl) >= 3 and lab.startswith(dl)) or lab.split(":")[0] == dl.split(":")[0]):
                if best is None or d["_ts"] > best["_ts"]:
                    best = d
        return best
    return level


def agents(rows, decisions):
    level = workflow_levels(decisions)
    by = collections.defaultdict(list)
    for d in rows:
        if d["scope"] == "subagent" and d["model"] in FAM:
            by[(d["session_id"], d.get("agent_id"))].append(d)
    out = []
    for (sess, aid), v in by.items():
        tier = collections.Counter(x.get("tier") for x in v).most_common(1)[0][0]
        a = {"traj": v, "model": v[0]["model"], "level": None}
        if tier in LEVEL_CFG:
            a["level"], a["src"] = tier, "tier"
        elif tier in MAIN_TIERS:
            a["src"] = "main-inherited"
        else:
            d = level(sess, aid, v[0]["_ts"])
            if d and d["chosen"] in LEVEL_CFG and d["model"] == a["model"]:
                a["level"], a["src"] = d["chosen"], "workflow"
            else:
                a["src"] = "unrouted"
        out.append(a)
    return out


def run_mix(ags, mix, basis):
    per = collections.Counter()
    for a in ags:
        if a["level"] is None:
            per["fixed:" + a["src"]] += replay(a["traj"], a["model"])
            continue
        frm = (FAM[a["model"]], LEVEL_CFG[a["level"]][1])
        to = mix.get(a["level"], LEVEL_CFG[a["level"]])
        s, o = mult(frm, to, basis) if to != frm else (1.0, 1.0)
        per[a["level"]] += replay(a["traj"], MODEL[to[0]], s, o)
    return per


def main_replay(rows, levels, basis):
    """Routed main sessions with the given main tiers on another model. Every
    stretch at such a tier switches (or, oracle, only those that pay back)."""
    sess = collections.defaultdict(list)
    for d in rows:
        if d["scope"] == "main" and d.get("routed"):
            sess[d["session_id"]].append(d)
    base = always = oracle = 0.0
    for v in sess.values():
        i = 0
        while i < len(v):
            j = i
            while j < len(v) and v[j].get("tier") == v[i].get("tier"):
                j += 1
            seg, t = v[i:j], v[i].get("tier")
            b = sum(req_price(x["model"], x["input_tokens"], x["output_tokens"], x["cache_read_input_tokens"], x["cache_creation_input_tokens"]) for x in seg if x["model"] in PRICES)
            base += b
            if t in levels and all(x["model"] == "claude-opus-5-5" for x in seg):
                fam, eff = levels[t]
                s, o = mult(("opus", t), (fam, eff), basis)
                first = dict(seg[0], input_tokens=0, cache_read_input_tokens=0, cache_creation_input_tokens=prompt(seg[0]))
                c = replay([first] + seg[1:], MODEL[fam], s, o, rewrites="all")
                if j < len(v):  # back on Opus: what the stretch added is written there too
                    g = (max(1, round(s * len(seg))) / len(seg)) ** (ALPHA - 1)
                    added = max(0, prompt(seg[-1]) - prompt(seg[0])) * g
                    if (v[j]["_ts"] - seg[0]["_ts"]).total_seconds() > 3600:
                        added = prompt(v[j])
                    c += added * (8.0 - 0.20) / 1e6
                always += c
                oracle += min(b, c)
            else:
                always += b
                oracle += b
            i = j
    return base, always, oracle


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--days", type=int, default=7)
    ap.add_argument("--end", help="end of the window, ISO time (default: now)")
    ap.add_argument("--basis", choices=["central", "pessimistic"], default="central")
    ap.add_argument("--main", action="store_true", help="Sonnet main tiers with switch costs")
    ap.add_argument("--configs", action="store_true", help="owner cost of every (model, effort), Opus high = 1")
    a = ap.parse_args()
    end = dt.datetime.fromisoformat(a.end) if a.end else dt.datetime.now().astimezone()
    rows, decisions = load(a.days, end)
    if a.main:
        for name, lv in (("low -> Sonnet high", {"low": ("sonnet", "high")}),
                         ("medium -> Sonnet xhigh", {"medium": ("sonnet", "xhigh")}),
                         ("low + medium", {"low": ("sonnet", "high"), "medium": ("sonnet", "xhigh")}),
                         ("low + medium + high", {"low": ("sonnet", "high"), "medium": ("sonnet", "xhigh"), "high": ("sonnet", "xhigh")})):
            b, al, orc = main_replay(rows, lv, a.basis)
            print(f"{name:24s} routed main ${b:8.0f}  switch every stretch {100 * (al / b - 1):+5.1f}%  oracle {100 * (orc / b - 1):+5.1f}%")
        return 0
    ags = agents(rows, decisions)
    if a.configs:
        routed = [x for x in ags if x["level"]]
        cost = {}
        for fam in MODEL:
            for e in EFF:
                cost[(fam, e)] = sum(replay(x["traj"], MODEL[fam], *mult((FAM[x["model"]], LEVEL_CFG[x["level"]][1]), (fam, e), a.basis)) for x in routed)
        ref = cost[("opus", "high")]
        for (fam, e), c in cost.items():
            print(f"{fam:7s} {e:7s} {c / ref:5.2f}")
        return 0
    base = None
    for name, mix in MIXES.items():
        per = run_mix(ags, mix, a.basis)
        routed = sum(v for k, v in per.items() if not k.startswith("fixed"))
        fixed = sum(v for k, v in per.items() if k.startswith("fixed"))
        base = base or (routed, fixed)
        print(f"{name:42s} routed ${routed:7.0f} ({100 * (routed / base[0] - 1):+5.1f}%)  all subagents ${routed + fixed:7.0f} ({100 * ((routed + fixed) / sum(base) - 1):+5.1f}%)")
    print("not routed by the subagent tiers: " + ", ".join(f"{k[6:]} ${v:.0f}" for k, v in run_mix(ags, {}, a.basis).items() if k.startswith("fixed")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
