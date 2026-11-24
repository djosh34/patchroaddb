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
	failed     bool
	reason     string
	cpuSeconds float64
	wall       time.Duration
	checksum   []byte
	log        []byte
	artifacts  map[string][]byte
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

// runSeed runs TestSeed with the seed in a gosim run, ended at its limits
// (seedLimits). With out set, a failing run's log, error and artifacts go to
// out/seed-<seed>/, with how to replay it.
func runSeed(
	t *testing.T, bin string, seed int64, planted string, l seedLimits, out string,
) seedOutcome {
	start := timeutil.Now()
	res, err := metatesting.ForTestBinary(t, bin).RunQuiet(l.runConfig("TestSeed", seed, planted))
	o := seedOutcome{wall: timeutil.Since(start)}
	switch {
	case err != nil:
		o.failed, o.reason = true, fmt.Sprintf("the test binary failed: %v", err)
	case res.Ended:
		// gosim's error says which limit the run passed.
		o.failed, o.reason = true, "timed out: "+res.Err
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
	if o.failed && out != "" {
		write := saveSeed(t, out, seed, planted, o.reason)
		if res != nil {
			// Every simulated machine's output, the nodes' logs included,
			// one JSON object a line.
			write("log.jsonl", res.LogOutput)
			write("log.txt", readableLog(res.LogOutput))
			for name, data := range res.Artifacts {
				write("artifact-"+filepath.Base(name), data)
			}
		}
	}
	return o
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
}

// TestCampaign runs the seeds SIM_FIRST to SIM_FIRST+SIM_COUNT-1, each a
// gosim run of its own with the limits of a seed (seedLimits).
// Every hundredth seed runs twice and must give the same run. Failing
// seeds go to SIM_OUT with their logs; the test fails if any seed failed.
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
	for seed := first; seed < first+count; seed++ {
		o := runSeed(t, bin, seed, planted, l, out)
		s.Run++
		s.CPUSeconds += o.cpuSeconds
		s.WallSecs += o.wall.Seconds()
		if !o.failed && seed%100 == 0 {
			again := runSeed(t, bin, seed, planted, l, "")
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

// TestSeedLimits checks the limits a seed runs with (seedLimits), on the
// translated test binary. A run that spins passes its limit of CPU time
// and fails as a timeout, with every goroutine's stack. A run whose
// process is starved of CPU, as on a VM that stalls, outlives its limit of
// CPU time in real time and still passes.
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

	// TestSpin never yields: only the limit of CPU time can end it here.
	res, err := metatesting.ForTestBinary(t, bin).RunQuiet(config("TestSpin", "SIM_SPIN=1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed || !res.Ended || !strings.Contains(res.Err, "passed its limit of 10s of CPU time") ||
		!strings.Contains(string(res.Artifacts["gosim-stacks.txt"]), "TestSpin") {
		t.Errorf("a spinning run: failed %v, ended %v, err %q", res.Failed, res.Ended, res.Err)
	}

	// TestSpinFor uses 6 s of CPU time. Its process is stopped for 20 s of
	// real time while it runs, longer than its limit of CPU time, which it
	// must not pass. The run saves whether its limit of real time, 15 s,
	// passed by the end of its spin: only a stop during the run makes it so.
	//
	// The test binary starts here, before the run: until the run, nothing
	// waits for it, so its pid stays its own, even should it end, until the
	// handle to it (a pidfd), which a recycled pid cannot redirect, is
	// open.
	runner := metatesting.ForTestBinary(t, bin)
	proc, err := os.FindProcess(childOf(t, os.Getpid(), bin))
	if err != nil {
		t.Fatal(err)
	}
	var starved *metatesting.RunResult
	var starvedErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		c := config("TestSpinFor", "SIM_SPIN=1", "SIM_SPIN_SECONDS=6")
		c.WallLimit = 15 * time.Second
		starved, starvedErr = runner.RunQuiet(c)
	}()
	// Stop it once it spins: it has used 3 s of CPU time.
	for deadline := timeutil.Now().Add(5 * time.Minute); cpuOf(t, proc.Pid) < 3*time.Second; time.Sleep(10 * time.Millisecond) {
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
	time.Sleep(20 * time.Second)
	if err := proc.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	<-done
	if starvedErr != nil {
		t.Fatal(starvedErr)
	}
	if starved.Failed || starved.Ended {
		t.Errorf("a starved run failed: ended %v, err %q", starved.Ended, starved.Err)
	}
	if got := string(starved.Artifacts["wall-passed"]); got != "true" {
		t.Errorf("the run's limit of real time had not passed by the end of its spin (wall-passed %q): its process was not stopped while it ran", got)
	}
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
		o := runSeed(t, bin, seed, bug, l, out)
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
		without := runSeed(t, bin, seed, "", l, withoutOut)
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
