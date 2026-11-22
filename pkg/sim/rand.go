// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package sim

import (
	"math/rand/v2"

	"github.com/cockroachdb/cockroach/pkg/util/syncutil"
)

// seedRand returns the one random generator of a seed: the schema, the
// data, the SQL, the faults, the fake S3's errors and the network's
// latencies all draw from it. Its source is locked, so the workers and the
// faults can draw at once; under gosim, which runs one goroutine at a
// time in an order the seed decides, the draws come in the same order on
// every run.
func seedRand(a, b uint64) *rand.Rand {
	return rand.New(&lockedSource{src: rand.NewPCG(a, b)})
}

// lockedSource is a rand.Source that is safe for concurrent use.
type lockedSource struct {
	mu  syncutil.Mutex
	src rand.Source
}

func (l *lockedSource) Uint64() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.src.Uint64()
}
