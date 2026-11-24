#!/usr/bin/env bash
# Shows the CPUs, memory and memory pressure of the machine a simulator job
# runs on, for sim.yml's jobs on Codex runners (#59).
#
# Usage:
#   build/sim-vm.sh report <label> [samples]
#     prints nproc, free -m and /proc/pressure/memory; with a samples file,
#     also the lowest available memory and the highest pressure it holds.
#   build/sim-vm.sh sample <samples>
#     appends the available memory and the memory pressure to the file
#     every 30 seconds, until it is killed.
set -u

case ${1:-} in
report)
  echo "== $2: $(date -u +%H:%M:%S)"
  echo "nproc: $(nproc)"
  free -m
  echo "/proc/pressure/memory:"
  cat /proc/pressure/memory 2>/dev/null || echo "(not available)"
  if [[ -s ${3:-} ]]; then
    awk '{ if (min == "" || $2 < min) { min = $2; at = $1 }
           for (i = 3; i <= NF; i++) if ($i ~ /^some_avg10=/) { v = substr($i, 12) + 0; if (v > max) { max = v; mat = $1 } } }
         END { printf "lowest available memory: %d MiB at %s; highest some avg10: %.2f%% at %s; %d samples\n", min, at, max, mat, NR }' "$3"
  fi
  ;;
sample)
  while :; do
    avail=$(awk '/^MemAvailable:/ { print int($2 / 1024) }' /proc/meminfo)
    psi=$(awk '{ printf "%s_%s ", $1, $2 }' /proc/pressure/memory 2>/dev/null)
    echo "$(date -u +%H:%M:%S) $avail $psi" >>"$2"
    sleep 30
  done
  ;;
*)
  echo "usage: build/sim-vm.sh report <label> [samples] | sample <samples>" >&2
  exit 2
  ;;
esac
