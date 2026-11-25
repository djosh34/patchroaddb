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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/cockroach/pkg/util/syncutil"
	"github.com/cockroachdb/errors"
)

// procs is the env of the nightly chaos run (build/ci/chaos.sh): real
// patchroaddb processes, each in a network namespace of its own, and a
// versitygw container for S3. Crashes are SIGKILL; a killed process keeps
// what it wrote, since the kernel does not lose it. Partitions and delays
// are iptables rules and netem in the node's namespace, and an S3 outage
// pauses the container.
type procs struct {
	bin      string
	dir      string
	endpoint string
	s3       string // the versitygw container
	user     string

	mu       syncutil.Mutex
	nodes    []*proc
	restored *proc
	failures []string
	// faultsOn are the faults that are on, by name.
	faultsOn map[string]bool
}

type proc struct {
	idx    int
	ns     string
	addr   string
	args   []string
	killed bool
	// done is closed when the process started last has ended.
	done chan struct{}
}

// The namespaces chaos.sh makes: n1 to n4 at 10.9.0.1 to 10.9.0.4 on eth0,
// bridged to the host at 10.9.0.254. n4 is for the restore.
func chaosNS(i int) (ns, addr string) { return fmt.Sprintf("n%d", i+1), fmt.Sprintf("10.9.0.%d", i+1) }

func (p *procs) root(args ...string) error {
	out, err := exec.Command("sudo", args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "sudo %s: %s", strings.Join(args, " "), out)
	}
	return nil
}

func (p *procs) nodeDir(n *proc) string { return filepath.Join(p.dir, n.ns) }

// start starts n's process and records it if it ends without a kill.
func (p *procs) start(n *proc) error {
	dir := p.nodeDir(n)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cfg := filepath.Join(dir, "sim.yaml")
	if err := os.WriteFile(cfg, []byte(backupConfig(p.endpoint)), 0o644); err != nil {
		return err
	}
	args := append([]string{"ip", "netns", "exec", n.ns, "sudo", "-u", p.user,
		"env", "AWS_ACCESS_KEY_ID=" + s3Key, "AWS_SECRET_ACCESS_KEY=" + s3Secret, "AWS_REGION=" + s3Region,
		p.bin}, n.args...)
	args = append(args, "--store=path="+filepath.Join(dir, "data")+",ballast-size=0",
		"--config="+cfg, "--log-dir="+filepath.Join(dir, "logs"))
	cmd := exec.Command("sudo", args...)
	f, err := os.OpenFile(filepath.Join(dir, "stderr.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return err
	}
	done := make(chan struct{})
	func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		n.killed = false
		n.done = done
	}()
	go func() {
		defer close(done)
		err := cmd.Wait()
		_ = f.Close()
		// A process that ends while not killed failed.
		if p.markKilled(n) {
			p.fail("%s stopped on its own: %v", n.ns, err)
		}
	}()
	return nil
}

func (p *procs) addrs() []string {
	var addrs []string
	for _, n := range p.nodes {
		addrs = append(addrs, n.addr)
	}
	return addrs
}

func (p *procs) s3Endpoint() string { return p.endpoint }

func (p *procs) crash(i int) bool { return p.kill(p.nodes[i]) }

// kill kills n's database process and waits until the sudo that started it
// has reaped it, so that a restart finds the store unlocked and the old
// process cannot change n's state. It reports false if that takes more than
// a minute. Only the database process is killed, found by its command line,
// which starts with the binary and names the node's store: a pid file would
// still name the previous process while a restarted one starts up, and the
// launchers stay alive so that their exit shows the database has ended.
// pkill runs again until then, which also catches a database that starts
// during the kill.
func (p *procs) kill(n *proc) bool {
	if !p.markKilled(n) {
		return false
	}
	p.mu.Lock()
	done := n.done
	p.mu.Unlock()
	if done == nil {
		// Never started.
		return true
	}
	pattern := "^" + regexp.QuoteMeta(p.bin) + " .*--store=path=" +
		regexp.QuoteMeta(filepath.Join(p.nodeDir(n), "data")) + ","
	deadline := time.After(time.Minute)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var lastErr error
	for {
		// pkill exits with status 1 when no process matches.
		if err := p.root("pkill", "-KILL", "-f", "--", pattern); err != nil && !noMatch(err) {
			lastErr = err
		}
		select {
		case <-done:
			return true
		case <-deadline:
			p.fail("%s did not end within a minute of the kill; last pkill error: %v", n.ns, lastErr)
			return false
		case <-tick.C:
		}
	}
}

// noMatch reports whether err is the exit status 1 of pkill, which means
// that no process matched.
func noMatch(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

// markKilled marks a running process killed and reports whether it ran.
func (p *procs) markKilled(n *proc) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n.killed {
		return false
	}
	n.killed = true
	return true
}

func (p *procs) fail(format string, args ...interface{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures = append(p.failures, fmt.Sprintf(format, args...))
}

func (p *procs) restart(i int, _ bool) {
	if err := p.start(p.nodes[i]); err != nil {
		p.fail("restarting %s: %v", p.nodes[i].ns, err)
	}
}

// partition drops the node's traffic to and from each other node, one rule
// for each; the host, where the workload runs, still reaches it. Turning
// it off removes only rules that are there.
func (p *procs) partition(i int, on bool) {
	n := p.nodes[i]
	if p.isFault(fmt.Sprintf("partition %d", i), on) {
		return
	}
	op := "-D"
	if on {
		op = "-I"
	}
	for j, o := range p.nodes {
		if j == i {
			continue
		}
		p.faultCmd(p.root("ip", "netns", "exec", n.ns, "iptables", op, "INPUT", "-s", o.addr, "-j", "DROP"))
		p.faultCmd(p.root("ip", "netns", "exec", n.ns, "iptables", op, "OUTPUT", "-d", o.addr, "-j", "DROP"))
	}
}

// delay delays everything node i sends; j does not matter here. A delay
// of 0 removes a delay that is there.
func (p *procs) delay(i, _ int, d time.Duration) {
	n := p.nodes[i]
	if d == 0 {
		if !p.isFault(fmt.Sprintf("delay %d", i), false) {
			p.faultCmd(p.root("ip", "netns", "exec", n.ns, "tc", "qdisc", "del", "dev", "eth0", "root"))
		}
		return
	}
	p.isFault(fmt.Sprintf("delay %d", i), true)
	p.faultCmd(p.root("ip", "netns", "exec", n.ns, "tc", "qdisc", "replace", "dev", "eth0", "root", "netem",
		"delay", strconv.Itoa(int(d.Milliseconds()))+"ms"))
}

func (p *procs) s3Faults(on bool) {
	if p.isFault("s3", on) {
		return
	}
	op := "unpause"
	if on {
		op = "pause"
	}
	if out, err := exec.Command("docker", op, p.s3).CombinedOutput(); err != nil {
		p.faultCmd(errors.Wrapf(err, "docker %s %s: %s", op, p.s3, out))
	}
}

// isFault records that the fault named is on or off, and reports whether
// it was so already, when there is nothing to do.
func (p *procs) isFault(name string, on bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.faultsOn == nil {
		p.faultsOn = make(map[string]bool)
	}
	was := p.faultsOn[name]
	p.faultsOn[name] = on
	return was == on
}

// faultCmd fails the test if a command that makes or ends a fault failed:
// the run would count a fault that did not happen.
func (p *procs) faultCmd(err error) {
	if err != nil {
		p.fail("a fault command failed: %v", err)
	}
}

func (p *procs) syncFaults(int, bool) bool { return false }

func (p *procs) startRestoreNode(ctx context.Context) (string, error) {
	ns, addr := chaosNS(len(p.nodes))
	n := &proc{idx: len(p.nodes), ns: ns, addr: addr, args: []string{
		"start-single-node", "--insecure", "--listen-addr=" + addr + ":26257", "--http-addr=" + addr + ":8080",
		"--cache=64MiB", "--max-sql-memory=128MiB",
	}}
	if err := p.start(n); err != nil {
		return "", err
	}
	p.restored = n
	return addr, waitSQL(ctx, addr, 2*time.Minute)
}

func (p *procs) failed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.failures...)
}

// stop kills every process at the end of the run.
func (p *procs) stop() {
	for _, n := range p.nodes {
		p.kill(n)
	}
	if p.restored != nil {
		p.kill(p.restored)
	}
}
