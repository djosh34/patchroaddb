#!/usr/bin/env bash
# Shows the CPUs, memory and memory pressure of the machine a simulator job
# runs on, for sim.yml's jobs on Codex runners (#59).
#
# Usage:
#   build/sim-vm.sh report <label> [samples]
#     prints nproc, free -m and /proc/pressure/memory; with a samples file,
#     also what the samples show: the lowest available memory, the longest
#     gap between two samples (a machine that stalls shows one), the CPU
#     time the hypervisor took (steal) and the major page faults.
#   build/sim-vm.sh sample <samples>
#     appends a sample to the file every 30 seconds, until it is killed:
#     the time in seconds, the available memory in MiB, the steal and
#     total CPU ticks, the major page faults and the memory pressure.
set -u

case ${1:-} in
report)
  echo "== $2: $(date -u +%H:%M:%S)"
  echo "nproc: $(nproc)"
  free -m
  echo "/proc/pressure/memory:"
  cat /proc/pressure/memory 2>/dev/null || echo "(not available)"
  if [[ -s ${3:-} ]]; then
    awk 'NR == 1 { t0 = $1; s0 = $3; c0 = $4; f0 = $5 }
         { if (min == "" || $2 < min) { min = $2; at = $1 - t0 }
           if (NR > 1 && $1 - prev > gap) { gap = $1 - prev; gat = prev - t0 }
           prev = $1; s = $3; c = $4; f = $5 }
         END { printf "%d samples over %d s; lowest available memory %d MiB at %d s; longest gap between samples %d s, after %d s; steal %.1f%% of CPU time; %d major page faults\n",
                 NR, prev - t0, min, at, gap, gat, (c > c0 ? 100 * (s - s0) / (c - c0) : 0), f - f0 }' "$3"
  fi
  ;;
sample)
  while :; do
    avail=$(awk '/^MemAvailable:/ { print int($2 / 1024) }' /proc/meminfo)
    cpu=$(awk '/^cpu / { t = 0; for (i = 2; i <= NF; i++) t += $i; print $9, t }' /proc/stat)
    majflt=$(awk '/^pgmajfault / { print $2 }' /proc/vmstat)
    psi=$(awk '{ printf "%s_%s ", $1, $2 }' /proc/pressure/memory 2>/dev/null)
    echo "$(date +%s) $avail $cpu $majflt $psi" >>"$2"
    sleep 30
  done
  ;;
*)
  echo "usage: build/sim-vm.sh report <label> [samples] | sample <samples>" >&2
  exit 2
  ;;
esac
