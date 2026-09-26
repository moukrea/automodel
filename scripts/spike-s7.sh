#!/bin/sh
# S7: does the injected ultracode opt-in trigger workflows as reliably as
# native `--effort ultracode`? Runs each prompt both ways in a scratch copy of
# a repo and counts Workflow tool calls in the transcripts.
#
#   scripts/spike-s7.sh <repo-dir> <prompts-file> [settings.json]
#
# prompts-file: one substantial prompt per line (10 recommended).
# The routed run forces the ultracode tier with a repo policy
# (min_tier = "ultracode"), so Jev's choice doesn't matter.
# COST: every prompt may start multi-agent workflows on your subscription.
set -eu
repo=$1 prompts=$2 settings=${3:-$HOME/.claude/settings.json}
work=$(mktemp -d)
count() { grep -c '"name":"Workflow"' "$1" 2>/dev/null || true; }
n=0
while IFS= read -r p; do
  [ -z "$p" ] && continue
  n=$((n + 1))
  for mode in native routed; do
    dir=$work/$mode-$n
    cp -r "$repo" "$dir"
    if [ $mode = routed ]; then
      printf 'min_tier = "ultracode"\n' > "$dir/.automodel.toml"
      out=$(cd "$dir" && claude -p --settings "$settings" --model jev --output-format json "$p" < /dev/null || true)
    else
      out=$(cd "$dir" && claude -p --effort ultracode --output-format json "$p" < /dev/null || true)
    fi
    sid=$(printf '%s' "$out" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("session_id",""))' 2>/dev/null || true)
    tr=$(ls ~/.claude/projects/*/"$sid".jsonl 2>/dev/null | head -1)
    printf '%s\t%s\tworkflows=%s\n' "$n" "$mode" "$(count "$tr")"
  done
done < "$prompts"
echo "scratch copies in $work"
