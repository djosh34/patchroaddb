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
	"regexp"
	"strconv"
	"strings"
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
}

// runSeed runs TestSeed with the seed in a gosim run, ended after wall of
// real time. With out set, a failing run's log, error and artifacts go to
// out/seed-<seed>/, with how to replay it.
func runSeed(
	t *testing.T, bin string, seed int64, planted string, wall time.Duration, out string,
) seedOutcome {
	start := timeutil.Now()
	// Every run gets the same variables, even an empty one: their number
	// changes the run.
	res, err := metatesting.ForTestBinary(t, bin).RunQuiet(&metatesting.RunConfig{
		Test:     "TestSeed",
		Seed:     seed,
		ExtraEnv: []string{"PATCHROADDB_PLANTED=" + planted},
		WallKill: wall,
	})
	o := seedOutcome{wall: timeutil.Since(start)}
	switch {
	case err != nil:
		o.failed, o.reason = true, fmt.Sprintf("the test binary failed: %v", err)
	case res.Ended:
		o.failed, o.reason = true, fmt.Sprintf("timed out after %s of real time: %s", wall, res.Err)
	case res.Failed:
		o.failed, o.reason = true, res.Err
		if o.reason == "" {
			o.reason = "the seed's checks failed"
		}
	}
	if res != nil {
		o.checksum = res.Checksum
		o.log = res.LogOutput
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

// structPointer is a pointer printed as a struct field, `{0x3202721a2ed8}`.
// patchroaddb's UnhandledRetryableError prints its kvpb.Error by value,
// whose fields hold one; the address depends on where the heap put it.
var structPointer = regexp.MustCompile(`\{0x[0-9a-f]+\}`)

// sameRun reports whether two runs of a seed, by their checksums and logs,
// are the same run. gosim's checksum covers what the run logs, so a printed
// pointer makes two equal runs differ. Their logs then differ in that
// pointer only: every line has its machine, goroutine, simulated time and
// gosim's step count, which differ too if the runs went apart.
func sameRun(checksumA, logA, checksumB, logB []byte) bool {
	if bytes.Equal(checksumA, checksumB) {
		return true
	}
	mask := []byte("{0x}")
	return bytes.Equal(structPointer.ReplaceAll(logA, mask), structPointer.ReplaceAll(logB, mask))
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
// gosim run of its own with SIM_WALL_MINUTES of real time (default 5).
// Every hundredth seed runs twice and must give the same run. Failing
// seeds go to SIM_OUT with their logs; the test fails if any seed failed.
func TestCampaign(t *testing.T) {
	bin := simEnv("SIM_BINARY")
	if bin == "" {
		skip.IgnoreLint(t, "SIM_BINARY is not set; build/sim-build.sh builds the binary, sim.yml runs campaigns")
	}
	first, count := envInt(t, "SIM_FIRST", 1), envInt(t, "SIM_COUNT", 1)
	wall := time.Duration(envInt(t, "SIM_WALL_MINUTES", 5)) * time.Minute
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
		o := runSeed(t, bin, seed, planted, wall, out)
		s.Run++
		s.CPUSeconds += o.cpuSeconds
		s.WallSecs += o.wall.Seconds()
		if !o.failed && seed%100 == 0 {
			// A second run that fails saves its log, as any failing run.
			again := runSeed(t, bin, seed, planted, wall, out)
			s.Replayed++
			switch {
			case again.failed:
				o.failed, o.reason = true, fmt.Sprintf("the seed passed, then failed when run again: %s", again.reason)
			case !sameRun(o.checksum, o.log, again.checksum, again.log):
				o.failed, o.reason = true, "the seed's second run differs from its first"
				if out != "" {
					// Both runs passed, so neither saved its log.
					write := saveSeed(t, out, seed, planted, o.reason)
					write("log.txt", readableLog(o.log))
					write("log-2.txt", readableLog(again.log))
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
	wall := time.Duration(envInt(t, "SIM_WALL_MINUTES", 5)) * time.Minute
	planted := simEnv("PATCHROADDB_PLANTED")
	out := simEnv("SIM_OUT")
	run := func() *metatesting.RunResult {
		res, err := metatesting.ForTestBinary(t, bin).RunQuiet(&metatesting.RunConfig{
			Test: "TestSeed", Seed: seed, ExtraEnv: []string{"PATCHROADDB_PLANTED=" + planted}, WallKill: wall,
		})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// write saves a run's log as name, and its artifacts, such as the
	// stacks of a run that passed its real time, beside it.
	write := func(name string, res *metatesting.RunResult) {
		if out == "" {
			return
		}
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
	first := run()
	write("log.txt", first)
	t.Logf("seed %d: failed %v %s", seed, first.Failed, first.Err)
	second := run()
	if !sameRun(first.Checksum, first.LogOutput, second.Checksum, second.LogOutput) || first.Failed != second.Failed {
		write("log-2.txt", second)
		t.Errorf("seed %d: the second run differs from the first: checksum %x, failed %v, against %x, failed %v",
			seed, first.Checksum, first.Failed, second.Checksum, second.Failed)
	}
	if first.Failed {
		t.Errorf("seed %d failed: %s", seed, first.Err)
	}
}

// plantedChecks is, for each planted bug, the start of the failure that
// the check meant for it reports. A seed catches the bug only with that
// failure: another failure, such as a node that loses its store, shows
// nothing about the bug.
var plantedChecks = map[string]string{
	planted.LostSync:      "ledger key ",
	planted.SkipRefresh:   "the accounts hold ",
	planted.WrongFilter:   "results differ at ",
	planted.InternalError: "internal error: ",
	planted.LostSegment:   "table ",
}

// TestPlanted runs seeds from SIM_FIRST with the planted bug
// PATCHROADDB_PLANTED on, at most SIM_COUNT of them, until one fails with
// the failure of the check meant for the bug (plantedChecks) while the
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
	wall := time.Duration(envInt(t, "SIM_WALL_MINUTES", 5)) * time.Minute
	out := simEnv("SIM_OUT")
	for seed := first; seed < first+count; seed++ {
		o := runSeed(t, bin, seed, bug, wall, out)
		if !o.failed {
			t.Logf("%s: seed %d passed", bug, seed)
			continue
		}
		if !failedWith(o.log, check) {
			t.Logf("%s: seed %d failed, but not with %q: %s", bug, seed, check, o.reason)
			continue
		}
		t.Logf("%s: seed %d failed with %q", bug, seed, check)
		// A seed that fails without the bug too shows nothing about the
		// bug; a campaign reports it. The next seed may.
		// Its logs go apart, not over those of the run with the bug.
		withoutOut := out
		if out != "" {
			withoutOut = filepath.Join(out, "without-bug")
		}
		without := runSeed(t, bin, seed, "", wall, withoutOut)
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
// starts with prefix.
func failedWith(log []byte, prefix string) bool {
	for _, line := range bytes.Split(log, []byte("\n")) {
		var m struct {
			Msg    string `json:"msg"`
			Method string `json:"method"`
		}
		if json.Unmarshal(line, &m) == nil && m.Method == "t.Error" && strings.HasPrefix(m.Msg, prefix) {
			return true
		}
	}
	return false
}

func TestSameRun(t *testing.T) {
	// The line that differed between two runs of seed 900.
	line := func(addr string) []byte {
		return []byte(`{"step":1813,"machine":"n1","msg":"failed to push during intent resolution: {<nil> 0 {` + addr + `} index:0"}` + "\n")
	}
	a, b := line("0x3202721a2ed8"), line("0x32025cd763c8")
	if !sameRun([]byte{1}, a, []byte{2}, b) {
		t.Error("logs that differ in an address are not the same run")
	}
	other := []byte(`{"step":1814,"machine":"n1","msg":"failed to push during intent resolution: {<nil> 0 {0x32025cd763c8} index:0"}` + "\n")
	// Hex values other than a struct's pointer count.
	txn := func(id string) []byte { return []byte(`{"step":1,"msg":"txn ` + id + `"}` + "\n") }
	if sameRun([]byte{1}, txn("0x3202721a2ed8"), []byte{2}, txn("0x32025cd763c8")) {
		t.Error("logs that differ in a hex value outside a struct are the same run")
	}
	if sameRun([]byte{1}, a, []byte{2}, other) {
		t.Error("logs that differ in a step are the same run")
	}
	if !sameRun([]byte{1}, a, []byte{1}, other) {
		t.Error("runs with one checksum are not the same run")
	}
}

func TestFailedWith(t *testing.T) {
	log := []byte(`{"msg":"seed summary","method":"t.Logf"}
{"msg":"results differ at 1579097408.960236209,12:","method":"t.Error","goroutine":16}
`)
	if !failedWith(log, plantedChecks[planted.WrongFilter]) {
		t.Error("the wrong-filter failure is not found")
	}
	if failedWith(log, plantedChecks[planted.LostSync]) {
		t.Error("a ledger failure is found where there is none")
	}
	if failedWith(log, "seed summary") {
		t.Error("a log line counts as a failure")
	}
}
