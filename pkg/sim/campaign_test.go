// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

//go:build !sim

package sim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/pkg/testutils/skip"
	"github.com/cockroachdb/cockroach/pkg/util/envutil"
	"github.com/cockroachdb/cockroach/pkg/util/planted"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/jellevandenhooff/gosim/metatesting"
)

// The tests in this file drive gosim runs of TestSeed from outside the
// simulation. They need the test binary that gosim translated
// (build/sim-build.sh) and run only when SIM_BINARY names it.

// seedOutcome is what one gosim run of TestSeed gave.
type seedOutcome struct {
	failed bool
	// timedOut is set when a time limit (seedLimits) ended the run.
	timedOut   bool
	reason     string
	cpuSeconds float64
	wall       time.Duration
	// steal is the share of the VM's CPU time, in percent, that its
	// hypervisor took while the run ran, or -1 when it is not known.
	steal     float64
	checksum  []byte
	log       []byte
	artifacts map[string][]byte
}

// seedLimits are the limits a seed's gosim run gets. cpu, the CPU time
// the run's process uses, ends a run that spins: it burns CPU. A run whose
// process is starved, as on a VM that stalls, uses little CPU, and only
// wall, the real time, ends it, much later.
type seedLimits struct {
	wall, cpu time.Duration
}

// limits returns the limits of a seed: SIM_WALL_MINUTES of real time
// (default 20) and SIM_CPU_SECONDS of CPU time (default 300). A healthy
// seed uses about 10 to 25 CPU-s.
func limits(t *testing.T) seedLimits {
	return seedLimits{
		wall: time.Duration(envInt(t, "SIM_WALL_MINUTES", 20)) * time.Minute,
		cpu:  time.Duration(envInt(t, "SIM_CPU_SECONDS", 300)) * time.Second,
	}
}

// runConfig is the gosim run of test with the seed and the planted bug,
// at the limits l. Every seed runs with it (runSeed, TestReplay), and
// TestSeedLimits checks its limits on other tests. Every run gets the same
// variables, even an empty one: their number changes the run.
func (l seedLimits) runConfig(test string, seed int64, planted string) *metatesting.RunConfig {
	return &metatesting.RunConfig{
		Test:     test,
		Seed:     seed,
		ExtraEnv: []string{"PATCHROADDB_PLANTED=" + planted},
		WallKill: l.wall,
		CPUKill:  l.cpu,
	}
}

// runSeed makes the gosim run c, of TestSeed with a seed, ended at its
// limits (seedLimits), and returns what it gave, with the VM's steal while
// it ran.
func runSeed(t *testing.T, bin string, c *metatesting.RunConfig) seedOutcome {
	start, stat := timeutil.Now(), readCPUStat()
	res, err := metatesting.ForTestBinary(t, bin).RunQuiet(c)
	return outcome(res, err, timeutil.Since(start), stealPercent(stat, readCPUStat()))
}

// outcome is what a gosim run gave: its result and error, real time and
// steal.
func outcome(res *metatesting.RunResult, err error, wall time.Duration, steal float64) seedOutcome {
	o := seedOutcome{wall: wall, steal: steal}
	switch {
	case err != nil:
		o.failed, o.reason = true, fmt.Sprintf("the test binary failed: %v", err)
	case res.Ended && strings.Contains(res.Err, " passed its limit of "):
		// gosim's error says which limit the run passed.
		o.failed, o.timedOut, o.reason = true, true, "timed out: "+res.Err
	case res.Ended:
		// gosim also ends the test binary when it cannot read the run's
		// CPU time: no limit ended the run.
		o.failed, o.reason = true, "the test binary ended: "+res.Err
	case res.Failed:
		o.failed, o.reason = true, res.Err
		if o.reason == "" {
			o.reason = "the seed's checks failed"
		}
	}
	if res != nil {
		o.checksum = res.Checksum
		o.log = res.LogOutput
		o.artifacts = res.Artifacts
		if v, err := strconv.ParseFloat(string(res.Artifacts["cpu-seconds"]), 64); err == nil {
			o.cpuSeconds = v
		}
	}
	return o
}

// countedRun runs a seed as it counts (#59), run making one gosim run of
// it. A run that a time limit ended runs once more, at once: a VM whose
// hypervisor takes its CPU, or that stalls, can make a healthy seed pass a
// limit, but a seed that hangs or spins does so again, as gosim runs it the
// same way each time. gosim ends the test binary of a run that a limit
// ended, so the second run is in a fresh process of the same binary. The
// seed fails if the second run also passes a limit, or fails in any other
// way. Any other failure counts at once: a failed check, a panic, an
// internal error, a test binary that failed or whose CPU time gosim
// could not read. So does a run that passed its limit of CPU time only
// between gosim's last two checks: no limit ended it, and it failed as it
// ended. keep gets the run that a limit ended before the second run
// starts, so that it is kept even should the second never run; first is
// that run, when the seed ran again.
func countedRun(run func() seedOutcome, keep func(first seedOutcome)) (o seedOutcome, first *seedOutcome) {
	o = run()
	if !o.timedOut {
		return o, nil
	}
	f := o
	keep(f)
	o = run()
	switch {
	case o.timedOut:
		o.reason = fmt.Sprintf("timed out twice; the first run %s; the second %s", f.reason, o.reason)
	case o.failed:
		o.reason = fmt.Sprintf("timed out, then failed when run again: %s; the first run %s", o.reason, f.reason)
	}
	return o, &f
}

// runCounted runs TestSeed with the seed as it counts (countedRun), at the
// limits l. With out set, the run that a time limit ended, when the seed
// ran again, goes to out/<firstDir>/seed-<seed>/ (save).
func runCounted(
	t *testing.T, bin string, l seedLimits, seed int64, planted, out, firstDir string,
) (o seedOutcome, first *seedOutcome) {
	o, first = countedRun(func() seedOutcome {
		return runSeed(t, bin, l.runConfig("TestSeed", seed, planted))
	}, func(first seedOutcome) {
		t.Logf("seed %d: a time limit ended its run, with %s steal, so it runs again; the first run %s",
			seed, fmtSteal(first.steal), first.reason)
		if out != "" {
			first.save(t, filepath.Join(out, firstDir), seed, planted)
		}
	})
	if first != nil {
		t.Logf("seed %d ran again: failed %v", seed, o.failed)
	}
	return o, first
}

// save writes a run of the seed to dir/seed-<seed>/: its error, how to
// replay it, every simulated machine's output, its artifacts, such as the
// stacks of a run that a time limit ended, and the VM's steal while it ran.
func (o seedOutcome) save(t *testing.T, dir string, seed int64, planted string) {
	write := saveSeed(t, dir, seed, planted, o.reason)
	// Every simulated machine's output, the nodes' logs included, one JSON
	// object a line.
	write("log.jsonl", o.log)
	write("log.txt", readableLog(o.log))
	for name, data := range o.artifacts {
		write("artifact-"+filepath.Base(name), data)
	}
	write("vm.txt", []byte(fmt.Sprintf("steal while the run ran: %s; real time: %.1f s\n", fmtSteal(o.steal), o.wall.Seconds())))
}

// cpuStat is the VM's CPU time so far, in clock ticks (/proc/stat): what
// its hypervisor took (steal), and in all.
type cpuStat struct {
	steal, total int64
	ok           bool
}

func readCPUStat() cpuStat {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuStat{}
	}
	line, _, _ := bytes.Cut(data, []byte("\n"))
	f := strings.Fields(string(line))
	// cpu user nice system idle iowait irq softirq steal [guest guest_nice],
	// where guest time is also in user and nice.
	if len(f) < 9 || f[0] != "cpu" {
		return cpuStat{}
	}
	var s cpuStat
	for i, v := range f[1:9] {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return cpuStat{}
		}
		s.total += n
		if i == 7 {
			s.steal = n
		}
	}
	s.ok = true
	return s
}

// stealPercent is the share of the VM's CPU time its hypervisor took from a
// to b, in percent, or -1 when it is not known.
func stealPercent(a, b cpuStat) float64 {
	if !a.ok || !b.ok || b.total <= a.total {
		return -1
	}
	return 100 * float64(b.steal-a.steal) / float64(b.total-a.total)
}

func fmtSteal(steal float64) string {
	if steal < 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.1f%%", steal)
}

// saveSeed makes out/seed-<seed>/ for a failing seed, with its error and
// how to replay it, and returns a function that writes more files there.
func saveSeed(
	t *testing.T, out string, seed int64, planted, reason string,
) func(name string, data []byte) {
	dir := filepath.Join(out, fmt.Sprintf("seed-%d", seed))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("error.txt", []byte(reason+"\n"))
	replay := fmt.Sprintf("build/sim-replay.sh %d", seed)
	if planted != "" {
		replay += " " + planted
	}
	write("replay.txt", []byte(fmt.Sprintf("commit %s\n%s\n", simEnv("GITHUB_SHA"), replay)))
	return write
}

// readableLog turns the JSON lines of a gosim run's log into text: the
// step, the machine and goroutine, the simulated time and the message.
func readableLog(jsonl []byte) []byte {
	var b bytes.Buffer
	for _, line := range bytes.Split(jsonl, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			b.Write(line)
			b.WriteByte('\n')
			continue
		}
		fmt.Fprintf(&b, "%v %v/%v %v %v", m["step"], m["machine"], m["goroutine"], m["time"], m["msg"])
		for k, v := range m {
			switch k {
			case "step", "machine", "goroutine", "time", "msg", "level", "source":
			default:
				fmt.Fprintf(&b, " %s=%v", k, v)
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// simEnv returns the environment variable name, or "".
func simEnv(name string) string {
	v, _ := envutil.ExternalEnvString(name, 0)
	return v
}

func envInt(t *testing.T, name string, def int64) int64 {
	v := simEnv(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		t.Fatalf("%s=%q: %v", name, v, err)
	}
	return n
}

// summary is what TestCampaign writes to SIM_OUT/summary.json: one per
// process, which the campaign's last job adds up.
type summary struct {
	First      int64            `json:"first"`
	Count      int64            `json:"count"`
	Run        int64            `json:"run"`
	Failed     map[int64]string `json:"failed"`
	CPUSeconds float64          `json:"cpu_seconds"`
	WallSecs   float64          `json:"wall_seconds"`
	Replayed   int64            `json:"replayed"`
	// SecondRuns are the seeds whose run a time limit ended, so that they
	// ran again (countedRun).
	SecondRuns []secondRun `json:"second_runs,omitempty"`
}

// secondRun is a seed whose run a time limit ended: why, the VM's steal
// during that run (-1 when not known), and whether the seed failed when it
// ran again.
type secondRun struct {
	Seed         int64   `json:"seed"`
	FirstReason  string  `json:"first_reason"`
	StealPercent float64 `json:"steal_percent"`
	Failed       bool    `json:"failed"`
}

// TestCampaign runs the seeds SIM_FIRST to SIM_FIRST+SIM_COUNT-1, each a
// gosim run of its own with the limits of a seed (seedLimits), counted as
// countedRun counts it. Every hundredth seed runs twice and must give the
// same run. Failing seeds go to SIM_OUT with their logs, and the runs a
// time limit ended to SIM_OUT/first-run/; the test fails if any seed
// failed.
func TestCampaign(t *testing.T) {
	bin := simEnv("SIM_BINARY")
	if bin == "" {
		skip.IgnoreLint(t, "SIM_BINARY is not set; build/sim-build.sh builds the binary, sim.yml runs campaigns")
	}
	first, count := envInt(t, "SIM_FIRST", 1), envInt(t, "SIM_COUNT", 1)
	l := limits(t)
	out := simEnv("SIM_OUT")
	planted := simEnv("PATCHROADDB_PLANTED")
	s := summary{First: first, Count: count, Failed: make(map[int64]string)}
	writeSummary := func() {
		if out == "" {
			return
		}
		data, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("summary-%d.json", first)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// noteSecondRun lists a seed that ran again in the summary, and counts
	// the real time of the run a time limit ended.
	noteSecondRun := func(seed int64, firstRun *seedOutcome, o seedOutcome) {
		if firstRun != nil {
			s.SecondRuns = append(s.SecondRuns, secondRun{
				Seed: seed, FirstReason: firstRun.reason, StealPercent: firstRun.steal, Failed: o.failed,
			})
			s.WallSecs += firstRun.wall.Seconds()
		}
	}
	for seed := first; seed < first+count; seed++ {
		o, firstRun := runCounted(t, bin, l, seed, planted, out, "first-run")
		noteSecondRun(seed, firstRun, o)
		if o.failed && out != "" {
			o.save(t, out, seed, planted)
		}
		s.Run++
		s.CPUSeconds += o.cpuSeconds
		s.WallSecs += o.wall.Seconds()
		if !o.failed && seed%100 == 0 {
			again, againFirst := runCounted(t, bin, l, seed, planted, out, "first-run-again")
			noteSecondRun(seed, againFirst, again)
			s.Replayed++
			switch {
			case again.failed:
				o.failed, o.reason = true, fmt.Sprintf("the seed passed, then failed when run again: %s", again.reason)
			case !bytes.Equal(o.checksum, again.checksum):
				o.failed, o.reason = true, "the seed's second run differs from its first"
			}
			if o.failed && out != "" {
				// The first run passed, so neither run saved its logs.
				write := saveSeed(t, out, seed, planted, o.reason)
				for name, run := range map[string]seedOutcome{"log": o, "log-2": again} {
					write(name+".jsonl", run.log)
					write(name+".txt", readableLog(run.log))
					for artifact, data := range run.artifacts {
						write(name+"-"+filepath.Base(artifact), data)
					}
				}
			}
		}
		if o.failed {
			s.Failed[seed] = o.reason
			t.Errorf("seed %d failed: %s", seed, o.reason)
		}
		t.Logf("seed %d: failed %v, %.1f CPU-s, %.1f s", seed, o.failed, o.cpuSeconds, o.wall.Seconds())
		writeSummary()
	}
	t.Logf("%d seeds, %d failed, %.1f CPU-s in all, %.2f CPU-s a seed", s.Run, len(s.Failed), s.CPUSeconds, s.CPUSeconds/float64(s.Run))
}

// TestReplay runs SIM_SEED twice and checks that both runs are the same
// (build/sim-replay.sh). It fails if they differ or if the seed failed. The
// first run's log goes to SIM_OUT/log.txt, and the second's to log-2.txt
// when the runs differ.
func TestReplay(t *testing.T) {
	bin := simEnv("SIM_BINARY")
	if bin == "" || simEnv("SIM_SEED") == "" {
		skip.IgnoreLint(t, "SIM_BINARY and SIM_SEED are not set; build/sim-replay.sh replays a seed")
	}
	seed := envInt(t, "SIM_SEED", 0)
	l := limits(t)
	planted := simEnv("PATCHROADDB_PLANTED")
	out := simEnv("SIM_OUT")
	run := func() *metatesting.RunResult {
		res, err := metatesting.ForTestBinary(t, bin).RunQuiet(l.runConfig("TestSeed", seed, planted))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	write := func(name string, res *metatesting.RunResult) {
		if out != "" {
			writeRun(t, out, name, res)
		}
	}
	first := run()
	write("log.txt", first)
	t.Logf("seed %d: failed %v %s", seed, first.Failed, first.Err)
	second := run()
	if !bytes.Equal(first.Checksum, second.Checksum) || first.Failed != second.Failed {
		write("log-2.txt", second)
		t.Errorf("seed %d: the second run differs from the first: checksum %x, failed %v, against %x, failed %v",
			seed, first.Checksum, first.Failed, second.Checksum, second.Failed)
	}
	if first.Failed {
		t.Errorf("seed %d failed: %s", seed, first.Err)
	}
}

// writeRun saves a run's log in out as name, and its artifacts, such as the
// stacks of a run that passed its real time, beside it.
func writeRun(t *testing.T, out, name string, res *metatesting.RunResult) {
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, name), readableLog(res.LogOutput), 0o644); err != nil {
		t.Fatal(err)
	}
	for artifact, data := range res.Artifacts {
		path := filepath.Join(out, strings.TrimSuffix(name, ".txt")+"-"+filepath.Base(artifact))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFirstStart runs TestFirstStartPowerLoss, a fixed scenario rather than
// a seed's: a power loss of every machine right after the cluster's first
// start, after which each must come back as the node it was.
func TestFirstStart(t *testing.T) {
	bin := simEnv("SIM_BINARY")
	if bin == "" {
		skip.IgnoreLint(t, "SIM_BINARY is not set; sim.yml runs this test")
	}
	res, err := metatesting.ForTestBinary(t, bin).RunQuiet(&metatesting.RunConfig{
		Test: "TestFirstStartPowerLoss", Seed: 1, ExtraEnv: []string{"PATCHROADDB_PLANTED="}, WallKill: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out := simEnv("SIM_OUT"); out != "" && (res.Failed || res.Ended) {
		writeRun(t, out, "first-start.txt", res)
	}
	if res.Failed || res.Ended {
		t.Errorf("TestFirstStartPowerLoss failed: %s", res.Err)
	}
}

// TestSeedLimits checks the limits a seed runs with (seedLimits), and how
// a seed counts (countedRun), on the translated test binary. A seed that
// spins passes its limit of CPU time in both its runs and fails as a
// timeout, with every goroutine's stack. A run whose process is starved of
// CPU, as on a VM that stalls, outlives its limit of CPU time in real time
// and still passes. A seed whose first run a stall ends past its limit of
// real time, and whose second passes, does not fail.
func TestSeedLimits(t *testing.T) {
	bin := simEnv("SIM_BINARY")
	if bin == "" {
		skip.IgnoreLint(t, "SIM_BINARY is not set; sim.yml runs this test")
	}
	// The limits of a seed, made short: 10 s of CPU time, and 2 minutes of
	// real time as the backstop, should the limit of CPU time not work.
	l := seedLimits{wall: 2 * time.Minute, cpu: 10 * time.Second}
	config := func(test string, extra ...string) *metatesting.RunConfig {
		c := l.runConfig(test, 1, "")
		c.ExtraEnv = append(c.ExtraEnv, extra...)
		return c
	}

	// TestSpin never yields: only the limit of CPU time can end it here. It
	// does so in both runs that a seed gets (countedRun), so it fails.
	// runs are the seed's runs, as they came, before countedRun counts them.
	var runs []seedOutcome
	kept := 0
	keep := func(seedOutcome) { kept++ }
	o, first := countedRun(func() seedOutcome {
		runs = append(runs, runSeed(t, bin, config("TestSpin", "SIM_SPIN=1")))
		return runs[len(runs)-1]
	}, keep)
	spun := func(o seedOutcome) bool {
		return o.timedOut && strings.HasPrefix(o.reason, "timed out: ") &&
			strings.Contains(o.reason, "passed its limit of 10s of CPU time") &&
			strings.Contains(string(o.artifacts["gosim-stacks.txt"]), "TestSpin")
	}
	if len(runs) != 2 || !spun(runs[0]) || !spun(runs[1]) || first == nil || kept != 1 ||
		!o.failed || !strings.HasPrefix(o.reason, "timed out twice; ") {
		t.Errorf("a spinning seed: %d runs (%+v), failed %v, reason %q", len(runs), runs, o.failed, o.reason)
	}

	// TestSpinFor uses 6 s of CPU time. Its process is stopped for 20 s of
	// real time while it runs, longer than its limit of CPU time, which it
	// must not pass. The run saves whether its limit of real time, 15 s,
	// passed by the end of its spin: a stop during the run makes it so, as
	// would a machine that starves it.
	c := config("TestSpinFor", "SIM_SPIN=1", "SIM_SPIN_SECONDS=6")
	c.WallLimit = 15 * time.Second
	starved, err := runStopped(t, bin, c, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if starved.Failed || starved.Ended {
		t.Errorf("a starved run failed: ended %v, err %q", starved.Ended, starved.Err)
	}
	if got := string(starved.Artifacts["wall-passed"]); got != "true" {
		t.Errorf("the run's limit of real time had not passed by the end of its spin (wall-passed %q): its process was not stopped while it ran", got)
	}

	// TestSpinFor again, as a seed whose first run a VM stalls past its
	// limit of real time: its process is stopped for 40 s, past its limit
	// of 30 s, which ends the run as soon as it goes on. The second run is
	// not stopped and passes, so the seed does not fail.
	stalled := config("TestSpinFor", "SIM_SPIN=1", "SIM_SPIN_SECONDS=6")
	stalled.WallKill = 30 * time.Second
	runs, kept = nil, 0
	o, first = countedRun(func() seedOutcome {
		if len(runs) == 0 {
			res, err := runStopped(t, bin, stalled, 40*time.Second)
			runs = append(runs, outcome(res, err, 0, -1))
		} else {
			runs = append(runs, runSeed(t, bin, stalled))
		}
		return runs[len(runs)-1]
	}, keep)
	// TestSpinFor saves wall-passed once its spin ends: a second run that
	// skipped it would pass without it.
	_, spunAgain := o.artifacts["wall-passed"]
	if len(runs) != 2 || !runs[0].timedOut || !strings.Contains(runs[0].reason, "passed its limit of 30s of real time") ||
		first == nil || kept != 1 || o.failed || !spunAgain {
		t.Errorf("a seed whose first run a stall ended: %d runs (%+v), failed %v, reason %q", len(runs), runs, o.failed, o.reason)
	}
}

// runStopped makes the gosim run c, and stops its process for stop once the
// run used 3 s of CPU time, as a VM that stalls would.
func runStopped(
	t *testing.T, bin string, c *metatesting.RunConfig, stop time.Duration,
) (*metatesting.RunResult, error) {
	// The test binary starts here, if it is not running yet, before the
	// run: until the run, nothing waits for it, so its pid stays its own,
	// even should it end, until the handle to it (a pidfd), which a
	// recycled pid cannot redirect, is open.
	runner := metatesting.ForTestBinary(t, bin)
	proc, err := os.FindProcess(childOf(t, os.Getpid(), bin))
	if err != nil {
		t.Fatal(err)
	}
	// FindProcess falls back to the bare pid when it cannot open a pidfd.
	if err := proc.WithHandle(func(uintptr) {}); err != nil {
		t.Fatalf("no pidfd for the test binary: %v", err)
	}
	// The binary may have run before: the run's CPU time is what it uses
	// from now.
	base := cpuOf(t, proc.Pid)
	var res *metatesting.RunResult
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, runErr = runner.RunQuiet(c)
	}()
	for deadline := timeutil.Now().Add(5 * time.Minute); cpuOf(t, proc.Pid)-base < 3*time.Second; time.Sleep(10 * time.Millisecond) {
		select {
		case <-done:
			t.Fatal("the run ended before its process was stopped")
		default:
		}
		if timeutil.Now().After(deadline) {
			t.Fatal("the run did not spin")
		}
	}
	if err := proc.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Signal(syscall.SIGCONT) }()
	time.Sleep(stop)
	if err := proc.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	<-done
	return res, runErr
}

// procStat returns the fields of /proc/<pid>/stat after the command, which
// is in parentheses: the state, the parent's pid, and so on.
func procStat(pid int) []string {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
}

// cpuOf returns the CPU time the process pid used so far.
func cpuOf(t *testing.T, pid int) time.Duration {
	f := procStat(pid)
	if len(f) < 13 {
		t.Fatalf("no process %d", pid)
	}
	// utime and stime, in clock ticks of 1/100 s.
	utime, _ := strconv.ParseInt(f[11], 10, 64)
	stime, _ := strconv.ParseInt(f[12], 10, 64)
	return time.Duration(utime+stime) * 10 * time.Millisecond
}

// childOf waits for a running child process of parent whose executable is
// bin, and returns its pid.
func childOf(t *testing.T, parent int, bin string) int {
	want, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := timeutil.Now().Add(time.Minute); timeutil.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		procs, err := os.ReadDir("/proc")
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range procs {
			pid, err := strconv.Atoi(p.Name())
			if err != nil {
				continue
			}
			fields := procStat(pid)
			if len(fields) < 2 || fields[0] == "Z" || fields[1] != strconv.Itoa(parent) {
				continue
			}
			if exe, err := os.Readlink(filepath.Join("/proc", p.Name(), "exe")); err == nil && exe == want {
				return pid
			}
		}
	}
	t.Fatalf("no running child of %d runs %s", parent, bin)
	return 0
}

// plantedChecks is, for each planted bug, whether a failing run's log
// shows a failure of the check meant for it. A seed catches the bug only
// with such a failure: another, such as a node that loses its store, shows
// nothing about the bug.
var plantedChecks = map[string]func(log []byte) bool{
	// Lost Raft log entries show as a lost acknowledged write, or as a
	// node's log.Fatal on Raft's own check of its log.
	planted.LostSync: func(log []byte) bool {
		return failedWith(log, prefix("ledger key ")) || raftLogFatal(log)
	},
	planted.SkipRefresh:   func(log []byte) bool { return failedWith(log, prefix("the accounts hold ")) },
	planted.WrongFilter:   func(log []byte) bool { return failedWith(log, prefix("results differ at ")) },
	planted.InternalError: func(log []byte) bool { return failedWith(log, prefix("internal error: ")) },
	planted.LostSegment:   func(log []byte) bool { return failedWith(log, prefix("table ")) },
}

// lostRaftLog is how Raft's check of a node's log reports entries the log
// should hold but does not.
const lostRaftLog = "is out of range [lastIndex("

// raftLogFatal reports whether a node stopped on a log.Fatal, and its own
// log, on its own machine, shows Raft's check of its log failing.
func raftLogFatal(log []byte) bool {
	fatal := make(map[string]bool) // the nodes that stopped on a log.Fatal
	raft := make(map[string]bool)  // the machines whose log shows lostRaftLog
	for _, line := range bytes.Split(log, []byte("\n")) {
		var m struct {
			Msg     string `json:"msg"`
			Method  string `json:"method"`
			Machine string `json:"machine"`
		}
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if node, rest, ok := strings.Cut(m.Msg, " "); m.Method == "t.Error" && ok &&
			strings.HasPrefix(rest, "stopped on its own: log.Fatal") {
			fatal[node] = true
		}
		if strings.Contains(m.Msg, lostRaftLog) {
			raft[m.Machine] = true
		}
	}
	for node := range fatal {
		if raft[node] {
			return true
		}
	}
	return false
}

func prefix(p string) func(string) bool {
	return func(msg string) bool { return strings.HasPrefix(msg, p) }
}

// TestPlanted runs seeds from SIM_FIRST with the planted bug
// PATCHROADDB_PLANTED on, at most SIM_COUNT of them, until one fails with
// a failure of the check meant for the bug (plantedChecks) while the
// same seed passes without the bug: then the bug counts as caught.
func TestPlanted(t *testing.T) {
	bin := simEnv("SIM_BINARY")
	bug := simEnv("PATCHROADDB_PLANTED")
	if bin == "" || bug == "" {
		skip.IgnoreLint(t, "SIM_BINARY and PATCHROADDB_PLANTED are not set; sim.yml checks the planted bugs")
	}
	check, ok := plantedChecks[bug]
	if !ok {
		t.Fatalf("no check is known for the planted bug %q", bug)
	}
	first, count := envInt(t, "SIM_FIRST", 1), envInt(t, "SIM_COUNT", 20)
	l := limits(t)
	out := simEnv("SIM_OUT")
	for seed := first; seed < first+count; seed++ {
		o, _ := runCounted(t, bin, l, seed, bug, out, "first-run")
		if o.failed && out != "" {
			o.save(t, out, seed, bug)
		}
		if !o.failed {
			t.Logf("%s: seed %d passed", bug, seed)
			continue
		}
		if !check(o.log) {
			t.Logf("%s: seed %d failed, but not in the bug's check: %s", bug, seed, o.reason)
			continue
		}
		t.Logf("%s: seed %d failed in the bug's check", bug, seed)
		// A seed that fails without the bug too shows nothing about the
		// bug; a campaign reports it. The next seed may.
		// Its logs go apart, not over those of the run with the bug.
		withoutOut := out
		if out != "" {
			withoutOut = filepath.Join(out, "without-bug")
		}
		without, _ := runCounted(t, bin, l, seed, "", withoutOut, "first-run")
		if without.failed && withoutOut != "" {
			without.save(t, withoutOut, seed, "")
		}
		if without.failed {
			t.Logf("%s: seed %d fails without the bug too: %s", bug, seed, without.reason)
			continue
		}
		t.Logf("%s: caught by seed %d, which passes without it", bug, seed)
		return
	}
	t.Fatalf("%s: not caught in %d seeds from %d", bug, count, first)
}

// failedWith reports whether a run's log has a test failure whose message
// check accepts.
func failedWith(log []byte, check func(string) bool) bool {
	for _, line := range bytes.Split(log, []byte("\n")) {
		var m struct {
			Msg    string `json:"msg"`
			Method string `json:"method"`
		}
		if json.Unmarshal(line, &m) == nil && m.Method == "t.Error" && check(m.Msg) {
			return true
		}
	}
	return false
}

func TestPlantedChecks(t *testing.T) {
	log := []byte(`{"msg":"seed summary","method":"t.Logf"}
{"msg":"results differ at 1579097408.960236209,12:","method":"t.Error","goroutine":16}
`)
	if !plantedChecks[planted.WrongFilter](log) {
		t.Error("the wrong-filter failure is not found")
	}
	if plantedChecks[planted.LostSync](log) {
		t.Error("a ledger failure is found where there is none")
	}
	if failedWith(log, prefix("seed summary")) {
		t.Error("a log line counts as a failure")
	}
	fatal := []byte(`{"msg":"n2 stopped on its own: log.Fatal, exit code 7","method":"t.Error"}` + "\n")
	raft := func(machine string) []byte {
		return []byte(`{"msg":"F200115 ... tocommit(627) is out of range [lastIndex(535)]. Was the raft log corrupted, truncated, or lost?","machine":"` + machine + `"}` + "\n")
	}
	if plantedChecks[planted.LostSync](fatal) {
		t.Error("a log.Fatal for another reason counts for lost-sync")
	}
	if !plantedChecks[planted.LostSync](append(raft("n2"), fatal...)) {
		t.Error("a log.Fatal on a Raft log out of range does not count for lost-sync")
	}
	if plantedChecks[planted.LostSync](append(raft("n3"), fatal...)) {
		t.Error("n2's log.Fatal counts for lost-sync on n3's Raft check")
	}
}

// TestCountedRun checks how a seed counts (countedRun): a run that a time
// limit ended runs once more, and only then fails, while any other
// failure counts at once.
func TestCountedRun(t *testing.T) {
	timedOut := seedOutcome{failed: true, timedOut: true, reason: "timed out: the run passed its limit of 5m0s of CPU time"}
	failedCheck := seedOutcome{failed: true, reason: "results differ"}
	passed := seedOutcome{}
	for _, tc := range []struct {
		name       string
		runs       []seedOutcome
		wantRuns   int
		wantFailed bool
		wantReason string
	}{
		{name: "passes", runs: []seedOutcome{passed}, wantRuns: 1},
		{name: "fails a check", runs: []seedOutcome{failedCheck}, wantRuns: 1, wantFailed: true, wantReason: "results differ"},
		{name: "timed out, then passes", runs: []seedOutcome{timedOut, passed}, wantRuns: 2},
		{name: "times out twice", runs: []seedOutcome{timedOut, timedOut}, wantRuns: 2, wantFailed: true,
			wantReason: "timed out twice; the first run " + timedOut.reason + "; the second " + timedOut.reason},
		{name: "timed out, then fails a check", runs: []seedOutcome{timedOut, failedCheck}, wantRuns: 2, wantFailed: true,
			wantReason: "timed out, then failed when run again: results differ; the first run " + timedOut.reason},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs, kept := 0, 0
			o, first := countedRun(func() seedOutcome {
				runs++
				if runs > len(tc.runs) {
					t.Fatalf("run %d: the seed ran again", runs)
				}
				if runs == 2 && kept != 1 {
					t.Fatal("the seed ran again before its first run was kept")
				}
				return tc.runs[runs-1]
			}, func(f seedOutcome) {
				kept++
				if f.reason != timedOut.reason {
					t.Errorf("kept %+v", f)
				}
			})
			if kept != tc.wantRuns-1 {
				t.Errorf("kept %d runs, want %d", kept, tc.wantRuns-1)
			}
			if runs != tc.wantRuns || o.failed != tc.wantFailed || (tc.wantFailed && o.reason != tc.wantReason) {
				t.Errorf("%d runs, failed %v, reason %q; want %d runs, failed %v, reason %q",
					runs, o.failed, o.reason, tc.wantRuns, tc.wantFailed, tc.wantReason)
			}
			if (first != nil) != (tc.wantRuns == 2) || (first != nil && first.reason != timedOut.reason) {
				t.Errorf("first run %+v", first)
			}
		})
	}
}

// TestOutcome checks which failed runs count as timed out: only those a
// limit ended. One whose CPU time gosim could not read counts at once, and
// so does one that passed its limit of CPU time only between gosim's last
// two checks, which gosim did not end.
func TestOutcome(t *testing.T) {
	for _, tc := range []struct {
		ended        bool
		err          string
		wantTimedOut bool
	}{
		{true, "the run of TestSeed with seed 7 passed its limit of 5m0s of CPU time; the stack of every goroutine is in the artifact gosim-stacks.txt", true},
		{true, "the run of TestSeed with seed 7 passed its limit of 20m0s of real time without yielding; the stack of every goroutine is in the artifact gosim-stacks.txt", true},
		{true, "the run of TestSeed with seed 7 its CPU time could not be read: no rusage; the stack of every goroutine is in the artifact gosim-stacks.txt", false},
		{false, "the run of TestSeed with seed 7 passed its limit of 5m0s of CPU time", false},
	} {
		o := outcome(&metatesting.RunResult{Failed: true, Ended: tc.ended, Err: tc.err}, nil, 0, -1)
		if !o.failed || o.timedOut != tc.wantTimedOut {
			t.Errorf("%q: failed %v, timed out %v; want timed out %v", tc.err, o.failed, o.timedOut, tc.wantTimedOut)
		}
		runs, wantRuns := 0, 1
		if tc.wantTimedOut {
			wantRuns = 2
		}
		countedRun(func() seedOutcome { runs++; return o }, func(seedOutcome) {})
		if runs != wantRuns {
			t.Errorf("%q: %d runs, want %d", tc.err, runs, wantRuns)
		}
	}
}

// TestStealPercent checks the share of the VM's CPU time its hypervisor
// took between two readings of /proc/stat.
func TestStealPercent(t *testing.T) {
	a := cpuStat{steal: 100, total: 1000, ok: true}
	if got := stealPercent(a, cpuStat{steal: 400, total: 2000, ok: true}); got != 30 {
		t.Errorf("steal %v%%, want 30%%", got)
	}
	if got := stealPercent(a, cpuStat{}); got != -1 {
		t.Errorf("steal %v%% without a second reading, want -1", got)
	}
	if s := readCPUStat(); s.ok && (s.total <= 0 || s.steal < 0 || s.steal > s.total) {
		t.Errorf("/proc/stat read as %+v", s)
	}
}
