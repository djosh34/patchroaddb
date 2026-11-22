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
	"testing"

	"github.com/cockroachdb/cockroach/pkg/sql/parser"
)

// TestGeneratedSQLParses checks that the generator writes valid SQL, so a
// seed does not spend its run on syntax errors.
func TestGeneratedSQLParses(t *testing.T) {
	for seed := uint64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0))
		tables := genSchema(rng)
		var stmts []string
		for _, tb := range tables {
			stmts = append(stmts, tb.create(), tb.write(rng), tb.write(rng), tb.write(rng))
		}
		for i := 0; i < 10; i++ {
			q := genQuery(rng, tables)
			stmts = append(stmts, q.render(func(table) string { return "" }))
			stmts = append(stmts, q.render(func(t table) string { return "@{FORCE_INDEX=primary}" }))
		}
		for _, s := range stmts {
			if _, err := parser.ParseOne(s); err != nil {
				t.Fatalf("seed %d: %s: %v", seed, s, err)
			}
		}
	}
}
