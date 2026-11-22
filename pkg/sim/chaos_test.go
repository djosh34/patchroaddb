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
	"os/exec"
	"os/user"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/cockroachdb/cockroach/pkg/backup/backuptest"
	"github.com/cockroachdb/cockroach/pkg/testutils/skip"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
)

// TestChaos is the nightly real-process chaos run (build/ci/chaos.sh): the
// scenario of TestSeed on real patchroaddb processes and versitygw, for
// CHAOS_MINUTES of real time, with random kills, partitions, network delay
// and S3 outages. It covers what the simulator does not model: the real
// binary, kernel, disk and network. CHAOS_SEED picks the scenario.
func TestChaos(t *testing.T) {
	bin := simEnv("CHAOS_BIN")
	if bin == "" {
		skip.IgnoreLint(t, "CHAOS_BIN is not set; build/ci/chaos.sh runs this test")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	seed := uint64(envInt(t, "CHAOS_SEED", timeutil.Now().UnixNano()))
	t.Logf("CHAOS_SEED=%d", seed)
	rng := seedRand(seed, 0)
	p := &procs{
		bin:      bin,
		dir:      simEnv("CHAOS_DIR"),
		endpoint: simEnv("CHAOS_S3"),
		s3:       simEnv("CHAOS_S3_CONTAINER"),
		user:     u.Username,
	}
	defer p.stop()
	var join []string
	for i := 0; i < 3; i++ {
		ns, addr := chaosNS(i)
		join = append(join, addr+":26257")
		p.nodes = append(p.nodes, &proc{idx: i, ns: ns, addr: addr})
	}
	for _, n := range p.nodes {
		n.args = []string{"start", "--insecure", "--listen-addr=" + n.addr + ":26257", "--http-addr=" + n.addr + ":8080",
			"--join=" + strings.Join(join, ","), "--cache=128MiB", "--max-sql-memory=128MiB"}
		if err = p.start(n); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(bin, "init", "--insecure", "--host="+p.nodes[0].addr+":26257").CombinedOutput()
	if err != nil {
		t.Fatalf("init: %v: %s", err, out)
	}
	ctx := context.Background()
	if _, err = backuptest.Client(t).CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s3Bucket)}); err != nil {
		t.Fatal(err)
	}
	for _, addr := range p.addrs() {
		if err = waitSQL(ctx, addr, 2*time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	s := newScenario(rng, p)
	s.runFor = time.Duration(envInt(t, "CHAOS_MINUTES", 10)) * time.Minute
	err = s.run(ctx)
	t.Logf("summary:\n%s", s.report())
	if err != nil {
		t.Error(err)
	}
	for _, f := range p.failed() {
		t.Error(f)
	}
	for _, f := range s.failures {
		t.Error(f)
	}
	if t.Failed() {
		t.Logf("the nodes' logs are in %s", p.dir)
	}
}
