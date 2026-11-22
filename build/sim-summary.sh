#!/usr/bin/env bash
# Adds up the summary-*.json files that TestCampaign wrote under DIR for a
# campaign of SEEDS seeds from FIRST, and fails unless they cover every
# seed and no seed failed. A seed with no summary never ran: it counts as
# a failure.
#
# Usage: build/sim-summary.sh <dir> <first> <seeds>
set -euo pipefail
dir=$1 first=$2 seeds=$3
for v in "$first" "$seeds"; do
  [[ $v =~ ^[0-9]+$ ]] || { echo "$v is not a number"; exit 1; }
done
mapfile -t files < <(find "$dir" -name 'summary-*.json' | sort)
if [[ ${#files[@]} == 0 ]]; then
  echo "no summaries in $dir: no seed ran"
  exit 1
fi
jq -r -s --argjson first "$first" --argjson seeds "$seeds" '
  {
    run: (map(.run) | add),
    covered: ([.[] | range(.first; .first + .run)] | map(select(. >= $first and . < $first + $seeds)) | unique | length),
    failed: (map(.failed | to_entries[]) | sort_by(.key | tonumber)),
    cpu: (map(.cpu_seconds) | add),
    wall: (map(.wall_seconds) | add),
    replayed: (map(.replayed) | add)
  } |
  "\(.run) seed runs, \(.covered) of \($seeds) seeds covered, \(.failed | length) failed, \(.replayed) replayed",
  "CPU per seed: \(.cpu / .run * 100 | round / 100) s; real time per seed: \(.wall / .run * 100 | round / 100) s",
  (.failed[] | "seed \(.key): \(.value | split("\n")[0])"),
  if .covered == $seeds and (.failed | length) == 0 then "the campaign is clean" else "THE CAMPAIGN IS NOT CLEAN" end
' "${files[@]}" | tee summary.txt
tail -1 summary.txt | grep -qx "the campaign is clean"
