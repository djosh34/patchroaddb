// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

// Package sim is patchroaddb's simulation testing. A seed runs a whole
// cluster in one process under gosim (github.com/jellevandenhooff/gosim,
// pinned to djosh34/gosim): three nodes, each a simulated machine with its
// own disk, address and globals that runs the real `patchroaddb start`,
// and a fake S3 server (fakes3). The seed decides everything: the
// schema, the rows that IMPORT loads from S3, the SQL statements, the
// faults, the order in which goroutines run, and time, which is simulated,
// so every sleep and timeout waits on the simulated clock. Replaying a
// seed on the same commit gives the same run (build/sim-replay.sh).
//
// The faults: crashes and restarts, with or without part of the unsynced
// writes, a power loss of the whole cluster, crashes in fsync, network
// partitions and delay, and S3 errors and stalls. Between faults the
// network has a base latency of 0.2 to 2 ms a pair of machines, from the
// seed: gosim delivers at once by default, and machines that answer each
// other at once would keep simulated time from passing.
//
// The checks:
//   - no node stops on its own and no statement returns an internal error.
//     A node that returns from its command or calls log.Fatal is seen as
//     such even while its fsyncs may crash it (cluster.go). One blind spot
//     is left: gosim does not tell why a machine stopped, so a node that
//     ends its process itself in another way while crashes in fsync are on
//     looks like one of those crashes, and is restarted;
//   - every machine keeps its store through the crashes and power losses:
//     after the faults it is the node it was before them;
//   - every acknowledged write to the ledger table survives the crashes;
//   - transfers between accounts keep the total, which holds only if the
//     transactions are serializable;
//   - every generated query runs, and those whose answer is fixed give the
//     same rows at one timestamp under other plans and settings: forced
//     indexes, the row engine, distributed or local execution, no join
//     reordering, no statistics. Rows are compared as multisets, in order
//     only where an ORDER BY of every column fixes it. A LIMIT without
//     such an ORDER BY, or a volatile function, leaves a query uncompared;
//   - a restore to a time during the workload, into a new cluster, equals
//     the source cluster at that time.
//
// Planted bugs (pkg/util/planted), built only with the tag planted, prove
// that the checks catch what they are for; sim.yml with planted=all checks
// each.
//
// The binaries: TestSeed (seed_test.go) is the seed, which runs only in
// the translated test binary. The native test binary drives it:
// TestCampaign runs a range of seeds, TestReplay one seed twice,
// TestPlanted the planted bugs and TestFirstStart a power loss right
// after the first start (TestFirstStartPowerLoss). build/sim-build.sh
// builds both, and
// .github/workflows/sim.yml runs campaigns. TestChaos runs the same
// scenario on real processes, every night (build/ci/chaos.sh).
//
// Bugs that a campaign finds are fixed in the bug-finding outcome, each
// with a regression test.
package sim
