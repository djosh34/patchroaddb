#!/usr/bin/env bash
# Replays one seed of the simulator on the current commit, twice, and fails
# if the two runs differ. A seed replays exactly only on the commit where it
# ran. The first run's log, every simulated node's included, goes to OUT
# (default bin/sim/replay-<seed>).
#
# Usage: build/sim-replay.sh <seed> [planted bug]
#
# The build and the run need a machine with 16 GB of memory, such as a
# GitHub runner.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
seed=${1:?usage: build/sim-replay.sh <seed> [planted bug]}
bug=${2:-}
if [[ -n $bug ]]; then
  export PLANTED=1
fi
"$root/build/sim-build.sh" "$root/bin/sim"
out=${OUT:-$root/bin/sim/replay-$seed}
cd "$root/pkg/sim"
SIM_BINARY="$root/bin/sim/sim-gosim.test" SIM_SEED=$seed SIM_OUT=$out PATCHROADDB_PLANTED=$bug \
  "$root/bin/sim/sim.test" -test.run '^TestReplay$' -test.v -test.timeout 60m
echo "the log of seed $seed is in $out/log.txt"
