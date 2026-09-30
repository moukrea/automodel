#!/usr/bin/env python3
"""Frontier tool for catalog.toml (spec §2.4).

Validates the catalog (same rules as `automodel catalog check`), computes the
dominance frontier of (model, effort) configs for one benchmark version, the
marginal returns along it, quasi-dominated cases, and alerts.

  frontier.py catalog.toml                  # Markdown
  frontier.py catalog.toml --json           # JSON (skill / CI)
  frontier.py catalog.toml --version v4.3.2 --today 2026-09-26

Exit status: 1 if the catalog is invalid, else 0. Stdlib only (Python 3.11+).
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import sys
import tomllib

STATUSES = ["active", "dominated", "excluded", "retired"]
# Window every model gets without meta.long_context_beta (mirrors catalog.StandardContext).
STANDARD_CONTEXT = 200_000
DEFAULT_MAIN_MIN_CONTEXT = 1_000_000
EFFORTS = ["low", "medium", "high", "xhigh", "max"]
SCOPES = ["main", "subagent"]
# Options of the relation question (mirrors catalog.Relations).
RELATIONS = ["continue", "extend", "inform", "side_question", "wrap_up", "new_task"]
META_REQUIRED = ["last_refresh", "benchmark", "benchmark_version", "jev_model",
                 "default_main_tier", "default_subagent_tier"]

# Quasi-dominance: B is on the frontier but some A costs at most
# QUASI_COST_RATIO × B's cost and scores QUASI_INDEX_GAP more points.
QUASI_COST_RATIO = 1.15
QUASI_INDEX_GAP = 5


def key(model: str, effort: str | None) -> str:
    return f"{model}@{effort}" if effort else model


def older_than(date: str | None, today: dt.date, days: int) -> bool:
    if not date:
        return False
    try:
        d = dt.date.fromisoformat(str(date))
    except ValueError:
        return False
    return (today - d).days > days


def dominates(a: dict, b: dict) -> bool:
    if a["index"] < b["index"] or a["cost_per_task"] > b["cost_per_task"]:
        return False
    strict = a["index"] > b["index"] or a["cost_per_task"] < b["cost_per_task"]
    ta, tb = a.get("time_per_task_s"), b.get("time_per_task_s")
    if ta is not None and tb is not None:
        if ta > tb:
            return False
        strict = strict or ta < tb
    return strict


def dominance(cat: dict, version: str) -> dict:
    configs: dict[str, dict] = {}
    duplicates, measured, ignored = [], set(), {}
    for m in cat.get("measurements", []):
        v = m.get("benchmark_version")
        if v != version:
            ignored[v] = ignored.get(v, 0) + 1
            continue
        k = key(m.get("model", ""), m.get("effort"))
        measured.add(m.get("model"))
        if k in configs:
            duplicates.append(k)
        configs[k] = m
    ordered = sorted(configs.items(), key=lambda kv: kv[1]["cost_per_task"])
    dominators = {k: [ka for ka, a in ordered if ka != k and dominates(a, b)] for k, b in ordered}
    return {"configs": ordered, "dominators": dominators, "measured": measured,
            "duplicates": duplicates, "ignored_versions": ignored}


def validate(cat: dict, today: dt.date, stale_days: int, dom: dict) -> list[dict]:
    issues: list[dict] = []
    err = lambda msg: issues.append({"level": "error", "message": msg})
    warn = lambda msg: issues.append({"level": "warning", "message": msg})

    meta = cat.get("meta", {})
    models = cat.get("models", {})
    tiers = cat.get("tiers", {})
    if not meta.get("schema"):
        err("meta.schema is required")
    for f in META_REQUIRED:
        if not meta.get(f):
            err(f"meta.{f} is required")
    if meta.get("default_main_tier") and meta["default_main_tier"] not in tiers.get("main", {}):
        err(f"meta.default_main_tier {meta['default_main_tier']!r} is not a main tier")
    if meta.get("default_subagent_tier") and meta["default_subagent_tier"] not in tiers.get("subagent", {}):
        err(f"meta.default_subagent_tier {meta['default_subagent_tier']!r} is not a subagent tier")

    for mid in sorted(models):
        m = models[mid]
        st = m.get("status", "")
        if st not in STATUSES:
            err(f"model {mid}: status {st!r} is not one of {STATUSES}")
        if st and st != "active" and not m.get("reason"):
            warn(f"model {mid}: status {st!r} without a reason")
        if st == "active":
            for f in ("api_id", "context", "price"):
                if not m.get(f):
                    err(f"model {mid}: active model needs {f}")
            lc = m.get("long_context", "")
            if m.get("context", 0) > STANDARD_CONTEXT and lc not in ("native", "beta"):
                err(f"model {mid}: context {m['context']} above {STANDARD_CONTEXT} needs long_context = \"native\" or \"beta\"")
            elif lc == "beta" and not meta.get("long_context_beta"):
                err(f"model {mid}: long_context = \"beta\" needs meta.long_context_beta")
            if m.get("per_turn_effort") and not meta.get("per_turn_effort_beta"):
                err(f"model {mid}: per_turn_effort needs meta.per_turn_effort_beta")
        for s in m.get("scopes", []):
            if s not in SCOPES:
                err(f"model {mid}: unknown scope {s!r}")
        de = m.get("default_effort")
        if de and de not in m.get("efforts", []):
            err(f"model {mid}: default_effort {de!r} not in efforts")

    for scope in sorted(tiers):
        if scope not in SCOPES:
            err(f"tiers.{scope}: unknown scope (want {SCOPES})")
            continue
        ranks: dict[int, str] = {}
        for tid in sorted(tiers[scope]):
            t = tiers[scope][tid]
            where = f"tier {scope}.{tid}"
            r = t.get("rank", 0)
            if r in ranks:
                err(f"{where}: rank {r} already used by {ranks[r]}")
            ranks[r] = tid
            if not t.get("criteria"):
                err(f"{where}: criteria is empty")
            m = models.get(t.get("model", ""))
            if m is None:
                err(f"{where}: unknown model {t.get('model')!r}")
                continue
            if m.get("status") != "active":
                err(f"{where}: model {t['model']} is {m.get('status')}, not active")
                continue
            if m.get("scopes") and scope not in m["scopes"]:
                err(f"{where}: model {t['model']} is restricted to scopes {m['scopes']}")
            eff, efforts = t.get("effort"), m.get("efforts", [])
            if eff and eff not in efforts:
                err(f"{where}: effort {eff!r} not supported by {t['model']} ({efforts})")
            if not eff and efforts:
                warn(f"{where}: no effort on {t['model']}, which supports {efforts}")
            min_main = meta.get("main_min_context") or DEFAULT_MAIN_MIN_CONTEXT
            if t.get("question") and (not t.get("no") or not 0 < t.get("threshold", 0) < 1):
                err(f"{where}: an asked tier needs no and a threshold between 0 and 1")
            if t.get("question") and tid == (meta.get("default_main_tier") if scope == "main" else meta.get("default_subagent_tier")):
                err(f"{where}: the default tier can't be an asked tier")
            mx = t.get("max_context", 0)
            if scope == "main" and m.get("context", 0) < min_main and (mx <= 0 or mx >= m.get("context", 0)):
                err(f"{where}: model {t['model']} has a {m.get('context', 0)}-token window; "
                    f"main tiers need at least {min_main} (meta.main_min_context), or a max_context below the window")
            default = meta.get("default_main_tier") if scope == "main" else meta.get("default_subagent_tier")
            if mx > 0 and tid == default:
                err(f"{where}: the default tier can't have a max_context")
            if scope == "subagent" and not m.get("alias"):
                err(f"{where}: model {t['model']} needs an alias (the Agent tool only accepts aliases)")

    for mid in sorted(cat.get("modes", {})):
        md = cat["modes"][mid]
        where = f"mode {mid}"
        if not md.get("scopes"):
            err(f"{where}: scopes is empty")
        for sc in md.get("scopes", []):
            if sc not in SCOPES:
                err(f"{where}: unknown scope {sc!r}")
            elif md.get("min_tier") and md["min_tier"] not in tiers.get(sc, {}):
                err(f"{where}: min_tier {md['min_tier']!r} is not a {sc} tier")
        th = md.get("threshold", 0)
        if not (0 < th <= 1):
            err(f"{where}: threshold must be in (0, 1]")
        if not (md.get("question") and md.get("yes") and md.get("no")):
            err(f"{where}: question, yes and no are required")
        if md.get("effort") and md["effort"] not in EFFORTS:
            err(f"{where}: unknown effort {md['effort']!r}")
    own = meta.get("benchmark_version", "")
    for scope in sorted(tiers):
        for tid in sorted(tiers[scope]):
            t = tiers[scope][tid]
            if t.get("model", "") in models and not t.get("cost") and not any(
                    ms.get("model") == t.get("model") and ms.get("effort") == t.get("effort")
                    and ms.get("benchmark_version") == own and ms.get("cost_per_task", 0) > 0
                    for ms in cat.get("measurements", [])):
                warn(f"tier {scope}.{tid}: no measurement for {t.get('model')}@{t.get('effort', '')} and no cost: its cost is interpolated")

    def tier_cost(t):
        for ms in cat.get("measurements", []):
            if (ms.get("model") == t.get("model") and ms.get("effort") == t.get("effort")
                    and ms.get("benchmark_version") == own and ms.get("cost_per_task", 0) > 0):
                return ms["cost_per_task"]
        return t.get("cost", 0) or 0

    # Costs must rise with rank (the policy reads rank as capability).
    for scope in sorted(tiers):
        prev = None
        for tid, t in sorted(tiers[scope].items(), key=lambda kv: kv[1].get("rank", 0)):
            c = tier_cost(t)
            if c <= 0:
                continue
            if prev and c < prev[1]:
                warn(f"tier {scope}.{tid} ranks above {prev[0]} but costs less ({c:.2f} vs {prev[1]:.2f}): costs must rise with rank")
            prev = (tid, c)
    if meta.get("underprovision_penalty", 0) < 0:
        err("meta.underprovision_penalty must be >= 0")

    questions = cat.get("questions", {})
    rel = questions.get("relation")
    if rel is not None:
        if not rel.get("question"):
            err("questions.relation: question is required")
        opts = rel.get("options", {})
        for oid in sorted(opts):
            if oid not in RELATIONS:
                err(f"questions.relation: unknown option {oid} (want {RELATIONS})")
            elif not opts[oid].get("what"):
                err(f"questions.relation.options.{oid}: what is required")
        for oid in RELATIONS:
            if oid not in opts:
                err(f"questions.relation: option {oid} is missing")
    expl = questions.get("explicit")
    if expl is not None:
        for name, want in (("question", "{x}"), ("off_question", "{x}"),
                           ("effort", "{v}"), ("mode", "{v}"), ("model", "{v}")):
            if expl.get(name) and want not in expl[name]:
                err(f"questions.explicit.{name} must contain {want}")
    for name in ("relation_separate_threshold", "explicit_threshold"):
        if not 0 <= meta.get(name, 0) <= 1:
            err(f"meta.{name} must be between 0 and 1")
    for name, present in (("meta.continues_threshold", bool(meta.get("continues_threshold"))),
                          ("meta.informs_threshold", bool(meta.get("informs_threshold"))),
                          ("questions.continues", "continues" in questions),
                          ("questions.informs", "informs" in questions)):
        if present:
            warn(f"{name}: deprecated, ignored (the relation question replaced it)")

    version = meta.get("benchmark_version", "")
    for scope in sorted(tiers):
        for tid in sorted(tiers[scope]):
            t = tiers[scope][tid]
            by = dom["dominators"].get(key(t.get("model", ""), t.get("effort")))
            if by:
                warn(f"tier {scope}.{tid}: config {key(t['model'], t.get('effort'))} is dominated by {by}")
    for d in dom["duplicates"]:
        warn(f"measurement {d} listed twice for {version}; the last one wins")
    for mid in sorted(models):
        if models[mid].get("status") == "active" and mid not in dom["measured"]:
            warn(f"model {mid}: active without a measurement in benchmark_version {version}")
    for ms in cat.get("measurements", []):
        m = models.get(ms.get("model", ""))
        k = key(ms.get("model", ""), ms.get("effort"))
        if m is None:
            warn(f"measurement {k}: unknown model")
            continue
        if ms.get("effort") and m.get("efforts") and ms["effort"] not in m["efforts"]:
            warn(f"measurement {k}: effort not in model efforts")
        if ms.get("benchmark_version") == version and older_than(ms.get("measured_at"), today, stale_days):
            warn(f"measurement {k}: measured_at {ms['measured_at']} is older than {stale_days} days")
    if older_than(meta.get("last_refresh"), today, stale_days):
        warn(f"meta.last_refresh {meta['last_refresh']} is older than {stale_days} days: "
             "run the refresh-model-catalog skill")
    return issues


def analyse(cat: dict, version: str, today: dt.date, stale_days: int) -> dict:
    dom = dominance(cat, version)
    # Validation always uses the catalog's own version.
    own = cat.get("meta", {}).get("benchmark_version", "")
    issues = validate(cat, today, stale_days, dom if version == own else dominance(cat, own))

    rows = []
    for k, m in dom["configs"]:
        rows.append({"config": k, "model": m["model"], "effort": m.get("effort"), "index": m["index"],
                     "cost_per_task": m["cost_per_task"], "time_per_task_s": m.get("time_per_task_s"),
                     "dominated_by": dom["dominators"][k]})
    frontier = [r for r in rows if not r["dominated_by"]]

    marginal = []
    for a, b in zip(frontier, frontier[1:]):
        marginal.append({"from": a["config"], "to": b["config"],
                         "index_gain": round(b["index"] - a["index"], 2),
                         "cost_multiplier": round(b["cost_per_task"] / a["cost_per_task"], 2)
                         if a["cost_per_task"] else None})

    quasi = []
    for b in frontier:
        for a in frontier:
            if a is b or a["cost_per_task"] <= b["cost_per_task"]:
                continue
            if a["cost_per_task"] <= b["cost_per_task"] * QUASI_COST_RATIO and a["index"] >= b["index"] + QUASI_INDEX_GAP:
                quasi.append({"config": b["config"], "versus": a["config"],
                              "cheaper_pct": round(100 * (1 - b["cost_per_task"] / a["cost_per_task"]), 1),
                              "index_delta": round(b["index"] - a["index"], 2)})

    alerts = []
    for v, n in sorted(dom["ignored_versions"].items(), key=lambda kv: str(kv[0])):
        alerts.append(f"{n} measurement(s) from benchmark_version {v} ignored (versions never compare)")
    errors = [i for i in issues if i["level"] == "error"]
    warnings = [i for i in issues if i["level"] == "warning"]
    refresh = any("older than" in w["message"] or "without a measurement" in w["message"] for w in warnings)
    if refresh:
        alerts.append("refresh recommended: stale or missing measurements")
    return {"benchmark_version": version, "valid": not errors, "errors": errors, "warnings": warnings,
            "configs": rows, "frontier": [r["config"] for r in frontier], "marginal": marginal,
            "quasi_dominated": quasi, "alerts": alerts, "refresh_recommended": refresh}


def markdown(r: dict) -> str:
    out = [f"# Frontier — benchmark {r['benchmark_version']}", ""]
    out += ["| Config | Index | $/task | Time/task | Status |", "|---|---:|---:|---:|---|"]
    for c in r["configs"]:
        t = f"{c['time_per_task_s']:g} s" if c["time_per_task_s"] is not None else "n/a"
        st = "frontier" if not c["dominated_by"] else "dominated by " + ", ".join(c["dominated_by"])
        out.append(f"| {c['config']} | {c['index']:g} | {c['cost_per_task']:.2f} | {t} | {st} |")
    if r["marginal"]:
        out += ["", "## Marginal returns along the frontier", "",
                "| Step | Index gain | Cost multiplier |", "|---|---:|---:|"]
        for m in r["marginal"]:
            out.append(f"| {m['from']} → {m['to']} | {m['index_gain']:+g} | ×{m['cost_multiplier']} |")
    if r["quasi_dominated"]:
        out += ["", "## Quasi-dominated (judgement needed)", ""]
        for q in r["quasi_dominated"]:
            out.append(f"- {q['config']}: {q['cheaper_pct']}% cheaper than {q['versus']} for "
                       f"{q['index_delta']:+g} index points")
    out += ["", f"## Validation: {'valid' if r['valid'] else 'INVALID'}", ""]
    for i in r["errors"] + r["warnings"]:
        out.append(f"- {i['level']}: {i['message']}")
    if r["alerts"]:
        out += ["", "## Alerts", ""] + [f"- {a}" for a in r["alerts"]]
    return "\n".join(out) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("catalog")
    ap.add_argument("--version", help="benchmark version (default: meta.benchmark_version)")
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--stale-days", type=int, default=60)
    ap.add_argument("--today", help="YYYY-MM-DD (default: today)")
    a = ap.parse_args()
    try:
        with open(a.catalog, "rb") as f:
            cat = tomllib.load(f)
    except (OSError, tomllib.TOMLDecodeError) as e:
        print(f"frontier: {e}", file=sys.stderr)
        return 2
    today = dt.date.fromisoformat(a.today) if a.today else dt.date.today()
    version = a.version or cat.get("meta", {}).get("benchmark_version", "")
    r = analyse(cat, version, today, a.stale_days)
    print(json.dumps(r, indent=2, ensure_ascii=False) if a.json else markdown(r), end="" if not a.json else "\n")
    return 0 if r["valid"] else 1


if __name__ == "__main__":
    sys.exit(main())
