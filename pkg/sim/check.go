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
	"fmt"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5/pgconn"
)

// This file holds the checks that need no simulator: how errors and
// results are judged.

// isInternal reports whether err is an internal error: a bug, unless a
// fault the harness injected explains it.
func isInternal(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// Class XX: internal error, data corrupted, index corrupted.
		return strings.HasPrefix(pgErr.Code, "XX")
	}
	return false
}

// normalize returns a result value as text that is equal for equal values:
// decimals lose trailing zeros, so 1.50 and 1.5 match.
func normalize(v string) string {
	if !strings.Contains(v, ".") || strings.ContainsAny(v, "eE") || strings.Trim(v, "-0123456789.") != "" {
		return v
	}
	v = strings.TrimRight(v, "0")
	v = strings.TrimSuffix(v, ".")
	if v == "-0" || v == "" {
		v = "0"
	}
	return v
}

// result is the rows of a query, each row its values joined by tabs.
type result []string

// sameResult compares two results: in order if ordered, else as
// multisets.
func sameResult(a, b result, ordered bool) bool {
	if len(a) != len(b) {
		return false
	}
	if !ordered {
		a, b = slices.Clone(a), slices.Clone(b)
		slices.Sort(a)
		slices.Sort(b)
	}
	return slices.Equal(a, b)
}

// diffResult describes how two results differ, for a failure report.
func diffResult(a, b result) string {
	count := func(r result) map[string]int {
		m := make(map[string]int)
		for _, row := range r {
			m[row]++
		}
		return m
	}
	ca, cb := count(a), count(b)
	var lines []string
	for row, n := range ca {
		if cb[row] != n {
			lines = append(lines, fmt.Sprintf("  %q: %d against %d", row, n, cb[row]))
		}
	}
	for row, n := range cb {
		if _, ok := ca[row]; !ok {
			lines = append(lines, fmt.Sprintf("  %q: 0 against %d", row, n))
		}
	}
	slices.Sort(lines)
	if len(lines) == 0 {
		return "  the same rows in another order"
	}
	return strings.Join(lines, "\n")
}

// ledgerKey is what the harness knows of one key of the ledger table: the
// last value whose write was acknowledged, and every value whose write's
// outcome is unknown. Such a write may take effect at any time, also after
// a later acknowledged write to the key, so its value stays allowed. Each
// value is written once, so an acknowledged write that is lost shows as an
// older acknowledged value or no row.
type ledgerKey struct {
	acked     int64
	hasAcked  bool
	ambiguous []int64
}

// allows reports whether v, or no row for !present, is a value the key
// may hold after all its writes.
func (k ledgerKey) allows(v int64, present bool) bool {
	if !present {
		return !k.hasAcked
	}
	if k.hasAcked && v == k.acked {
		return true
	}
	return slices.Contains(k.ambiguous, v)
}
