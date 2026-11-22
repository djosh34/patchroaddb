// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

//go:build sim

package sim

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/cockroach/pkg/cli"
	"github.com/cockroachdb/cockroach/pkg/cli/exit"
	"github.com/cockroachdb/cockroach/pkg/sim/fakes3"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/syncutil"
	"github.com/cockroachdb/errors"
	"github.com/jellevandenhooff/gosim"
)

// The simulated network. Each node is a gosim machine of its own, with its
// own globals, disk and address; the test runs on the main machine.
const (
	s3Addr      = "10.0.2.1"
	initAddr    = "10.0.3.1"
	restoreAddr = "10.0.1.1"
	httpPort    = 8080
)

func nodeAddr(i int) string { return fmt.Sprintf("10.0.0.%d", i+1) }

// node is one patchroaddb process on a machine.
type node struct {
	idx  int
	addr string
	m    gosim.Machine
	// state and syncFaults are guarded by cluster.mu.
	state nodeState
	// syncFaults is set while the node's fsyncs may crash it.
	syncFaults bool
	// exited, if set, says why the node's process ended on its own.
	exited string
}

type nodeState int

const (
	up nodeState = iota
	// crashed: the harness crashed the node and restarts it.
	crashed
	// crashedInSync: an fsync crashed the node, and watch restarts it.
	crashedInSync
)

// cluster is the simulated cluster, the fake S3 server and what the
// harness knows of them.
type cluster struct {
	nodes []*node
	s3    *fakes3.Server
	// rng draws the network's latencies.
	rng *rand.Rand
	// latency is the base latency between two machines, by their addresses
	// in order; a delay fault replaces it for a while.
	latency map[[2]string]time.Duration

	mu syncutil.Mutex
	// failures are the nodes that stopped without the harness crashing
	// them: a crash of patchroaddb.
	failures []string
}

// The --config of every node: only its backup section, since the nodes
// run insecure. Full backups every 20 s of simulated time, archive
// segments every 2 s.
var nodeConfig = backupConfig("http://" + s3Addr + ":9000")

// s3Env gives a machine the S3 credentials of the fake server, which
// checks none, and keeps the AWS SDK from looking elsewhere for them.
func s3Env() {
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":         s3Key,
		"AWS_SECRET_ACCESS_KEY":     s3Secret,
		"AWS_REGION":                s3Region,
		"AWS_EC2_METADATA_DISABLED": "true",
	} {
		// Setenv fails only on an invalid name.
		_ = os.Setenv(k, v)
	}
}

// runCLI runs patchroaddb with the arguments, on the machine that calls
// it, the way the binary's main does. It does not return: cli.Main exits.
func runCLI(args ...string) {
	os.Args = append([]string{"patchroaddb"}, args...)
	cli.Main()
}

func startArgs(addr, join string) []string {
	return []string{
		"start", "--insecure",
		"--listen-addr=" + addr + fmt.Sprintf(":%d", sqlPort),
		"--http-addr=" + addr + fmt.Sprintf(":%d", httpPort),
		"--join=" + join,
		"--store=path=/data,ballast-size=0",
		// IMPORT reserves 32 MiB of SQL memory on each node for its
		// bulk writer.
		"--cache=32MiB", "--max-sql-memory=256MiB",
		"--config=/sim.yaml",
		"--logtostderr=INFO", "--log-dir=",
	}
}

// nodeMain is the main function of a node's machine: it writes the config
// file, if it is not there from an earlier start, and starts patchroaddb.
// It records a node that exits, or calls log.Fatal, as stopped on its own:
// a failure, also while the node's fsyncs may crash it, which watch could
// otherwise not tell apart.
func (c *cluster) nodeMain(nd *node, args []string) func() {
	return func() {
		s3Env()
		if err := os.WriteFile("/sim.yaml", []byte(nodeConfig), 0o644); err != nil {
			c.exited(nd, fmt.Sprintf("writing the config: %v", err))
			return
		}
		log.SetExitFunc(false /* hideStack */, func(code exit.Code) {
			c.exited(nd, fmt.Sprintf("log.Fatal, exit code %s", code))
			exit.WithCode(code)
		})
		os.Args = append([]string{"patchroaddb"}, args...)
		code := cli.MainCode()
		c.exited(nd, fmt.Sprintf("exit code %s", code))
	}
}

// exited records that the node's process ended on its own, and why.
func (c *cluster) exited(nd *node, why string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nd.exited = why
}

// startCluster starts the fake S3 server and n nodes, initializes the
// cluster and waits until SQL works on every node. rng draws the network's
// latencies.
func startCluster(ctx context.Context, n int, s3 *fakes3.Server, rng *rand.Rand) (*cluster, error) {
	c := &cluster{s3: s3, rng: rng, latency: make(map[[2]string]time.Duration)}
	s3.CreateBucket(s3Bucket)
	gosim.NewMachine(gosim.MachineConfig{
		Label: "s3",
		Addr:  netip.MustParseAddr(s3Addr),
		MainFunc: func() {
			srv := &http.Server{Addr: s3Addr + ":9000", Handler: s3}
			fmt.Fprintf(os.Stderr, "fake S3: %v\n", srv.ListenAndServe())
		},
	})

	var join []string
	for i := 0; i < n; i++ {
		join = append(join, fmt.Sprintf("%s:%d", nodeAddr(i), sqlPort))
	}
	for i := 0; i < n; i++ {
		nd := &node{idx: i, addr: nodeAddr(i)}
		nd.m = gosim.NewMachine(gosim.MachineConfig{
			Label:    fmt.Sprintf("n%d", i+1),
			Addr:     netip.MustParseAddr(nd.addr),
			MainFunc: c.nodeMain(nd, startArgs(nd.addr, strings.Join(join, ","))),
		})
		c.nodes = append(c.nodes, nd)
		go c.watch(nd)
	}

	addrs := []string{s3Addr, initAddr}
	for _, nd := range c.nodes {
		addrs = append(addrs, nd.addr)
	}
	c.addLatencies(addrs)

	// patchroaddb init, from a machine of its own: the CLI retries until
	// the first node answers, and exits when it is done. A node that stops
	// meanwhile is a failure; the harness does not wait for init forever.
	initM := gosim.NewMachine(gosim.MachineConfig{
		Label: "init",
		Addr:  netip.MustParseAddr(initAddr),
		MainFunc: func() {
			runCLI("init", "--insecure", fmt.Sprintf("--host=%s:%d", nodeAddr(0), sqlPort))
		},
	})
	initDone := make(chan struct{})
	go func() {
		initM.Wait()
		close(initDone)
	}()
	select {
	case <-initDone:
	case <-time.After(2 * time.Minute):
		initM.Crash()
		<-initDone
	}
	if failed := c.failed(); len(failed) > 0 {
		return c, errors.Newf("while the cluster started: %s", strings.Join(failed, "; "))
	}

	for _, nd := range c.nodes {
		if err := waitSQL(ctx, nd.addr, 2*time.Minute); err != nil {
			return c, err
		}
	}
	return c, nil
}

// addLatencies gives each pair of the machines a base latency of 0.2 to 2
// ms, drawn from the seed. gosim's network delivers at once by default;
// with no latency, two machines that answer each other at once never let
// simulated time pass, and the seed runs until its real time is up.
func (c *cluster) addLatencies(addrs []string) {
	for i, a := range addrs {
		for _, b := range addrs[i+1:] {
			if _, ok := c.latency[pair(a, b)]; ok {
				continue
			}
			d := time.Duration(200+c.rng.IntN(1800)) * time.Microsecond
			c.latency[pair(a, b)] = d
			_ = gosim.SetDelay(a, b, d)
		}
	}
}

func pair(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// watch records a node that stops on its own: a crash of patchroaddb.
// A node that an fsync crashed while its sync faults were on is restarted
// after a second.
func (c *cluster) watch(nd *node) {
	for {
		nd.m.Wait()
		switch c.stopped(nd) {
		case up:
			return
		case crashedInSync:
			time.Sleep(time.Second)
			nd.m.RestartWithPartialDisk()
			c.setState(nd, up)
		case crashed:
			// The harness restarts it; Wait blocks until the next stop.
			for c.state(nd) == crashed {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

// stopped records that the node's machine stopped, and returns why: up for
// a stop the harness did not cause, which is a failure.
func (c *cluster) stopped(nd *node) nodeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if nd.exited != "" {
		// The process ended itself, whatever the faults: a crash of
		// patchroaddb. The node stays down.
		c.failures = append(c.failures, fmt.Sprintf("n%d stopped on its own: %s", nd.idx+1, nd.exited))
		return up
	}
	if nd.state == up && nd.syncFaults {
		nd.state = crashedInSync
	}
	if nd.state == up {
		c.failures = append(c.failures, fmt.Sprintf("n%d stopped on its own", nd.idx+1))
	}
	return nd.state
}

func (c *cluster) state(nd *node) nodeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return nd.state
}

func (c *cluster) setState(nd *node, s nodeState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nd.state = s
}

// markCrashed moves an up node to crashed and reports whether it was up.
func (c *cluster) markCrashed(nd *node) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if nd.state != up {
		return false
	}
	nd.state = crashed
	return true
}

// crash stops the node abruptly: writes it has not synced are lost. It
// reports false, and does nothing, if the node is down already.
func (c *cluster) crash(i int) bool {
	nd := c.nodes[i]
	if !c.markCrashed(nd) {
		return false
	}
	nd.m.Crash()
	return true
}

// restart starts a node that crash stopped. With partial, a random part of
// the writes it had not synced reaches the disk.
func (c *cluster) restart(i int, partial bool) {
	nd := c.nodes[i]
	c.exited(nd, "")
	if partial {
		nd.m.RestartWithPartialDisk()
	} else {
		nd.m.Restart()
	}
	c.setState(nd, up)
}

// syncFaults turns on or off crashes of the node in fsync.
func (c *cluster) syncFaults(i int, on bool) bool {
	nd := c.nodes[i]
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		nd.syncFaults = on
	}()
	nd.m.SetSometimesCrashOnSync(on)
	return true
}

func (c *cluster) addrs() []string {
	var addrs []string
	for _, nd := range c.nodes {
		addrs = append(addrs, nd.addr)
	}
	return addrs
}

func (c *cluster) s3Endpoint() string { return "http://" + s3Addr + ":9000" }

func (c *cluster) partition(i int, on bool) {
	for j, o := range c.nodes {
		if j != i {
			_ = gosim.SetConnected(c.nodes[i].addr, o.addr, !on)
		}
	}
}

// delay sets the delay between nodes i and j; 0 restores their base
// latency.
func (c *cluster) delay(i, j int, d time.Duration) {
	a, b := c.nodes[i].addr, c.nodes[j].addr
	if d == 0 {
		d = c.latency[pair(a, b)]
	}
	_ = gosim.SetDelay(a, b, d)
}

func (c *cluster) s3Faults(on bool) {
	var f fakes3.Faults
	if on {
		f = fakes3.Faults{ErrorRate: 0.3, StallRate: 0.2, Stall: 5 * time.Second}
	}
	c.s3.SetFaults(f)
}

// startRestoreNode starts a new single-node cluster for a restore.
func (c *cluster) startRestoreNode(ctx context.Context) (string, error) {
	nd := &node{idx: len(c.nodes), addr: restoreAddr}
	args := startArgs(nd.addr, "")
	args[0] = "start-single-node"
	args = slices.DeleteFunc(args, func(a string) bool { return strings.HasPrefix(a, "--join=") })
	nd.m = gosim.NewMachine(gosim.MachineConfig{
		Label:    "restore",
		Addr:     netip.MustParseAddr(nd.addr),
		MainFunc: c.nodeMain(nd, args),
	})
	go c.watch(nd)
	c.addLatencies(append([]string{s3Addr, nd.addr}, c.addrs()...))
	return nd.addr, waitSQL(ctx, nd.addr, 2*time.Minute)
}

func (c *cluster) failed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.failures...)
}
