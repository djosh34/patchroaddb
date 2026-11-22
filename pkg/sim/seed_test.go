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
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/pkg/sim/fakes3"
	"github.com/jellevandenhooff/gosim"
)

// TestSeed is one seed of the simulation (doc.go). gosim runs it with the
// seed, which decides everything: the generated schema, data, SQL and
// faults, the order of the goroutines and the simulated time.
func TestSeed(t *testing.T) {
	gosim.SetSimulationTimeout(30 * time.Minute)
	cpu0, _ := gosim.CPUTime()
	// The run's random stream is the seed's; the generator's own stream
	// starts from it before anything else draws, so it depends on the seed
	// alone.
	rng := seedRand(rand.Uint64(), rand.Uint64())
	s3 := fakes3.New(rng)
	s := newScenario(rng, nil)
	s3Env()
	ctx := context.Background()
	// cpu writes the CPU time since the start as the artifact name. The
	// value differs between runs, so it goes to an artifact, never to the
	// log or the run.
	cpu := func(name string) {
		if cpu1, err := gosim.CPUTime(); err == nil {
			gosim.WriteArtifact(name, []byte(fmt.Sprintf("%.3f", (cpu1-cpu0).Seconds())))
		}
	}
	s.phase = func(name string) { cpu("cpu-seconds-after-" + name) }
	defer func() {
		t.Logf("seed summary:\n%s", s.report())
		cpu("cpu-seconds")
	}()

	c, err := startCluster(ctx, 3, s3, rng)
	if err != nil {
		t.Fatalf("starting the cluster: %v", err)
	}
	s.phase("start")
	s.env = c
	if err := s.run(ctx); err != nil {
		t.Error(err)
	}
	for _, f := range c.failed() {
		t.Error(f)
	}
	for _, f := range s.failures {
		t.Error(f)
	}
}

// TestFirstStartPowerLoss is the regression test of a node that lost its
// whole store in a power loss soon after its first start (#59): the store
// directory and its parent were created without syncing their parents. It
// starts the cluster, cuts the power of every machine as soon as SQL works,
// with none of the writes they had not synced, and checks that each machine
// comes back as the node it was.
func TestFirstStartPowerLoss(t *testing.T) {
	gosim.SetSimulationTimeout(10 * time.Minute)
	rng := seedRand(rand.Uint64(), rand.Uint64())
	s3Env()
	ctx := context.Background()
	c, err := startCluster(ctx, 3, fakes3.New(rng), rng)
	if err != nil {
		t.Fatalf("starting the cluster: %v", err)
	}
	var ids []int64
	for _, addr := range c.addrs() {
		id, err := nodeID(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for i := range c.nodes {
		c.crash(i)
	}
	for i := range c.nodes {
		c.restart(i, false /* partial */)
	}
	for i, addr := range c.addrs() {
		if err := waitSQL(ctx, addr, 3*time.Minute); err != nil {
			t.Fatal(err)
		}
		id, err := nodeID(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		if id != ids[i] {
			t.Errorf("%s came back as node %d, not node %d: it lost its store", addr, id, ids[i])
		}
	}
	for _, f := range c.failed() {
		t.Error(f)
	}
}
