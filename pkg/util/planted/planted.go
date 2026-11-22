// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

// Package planted switches on deliberate bugs that prove the simulator's
// checks work. The bugs exist only in a build with the tag planted, which
// patchroaddb never uses; without it, On is a constant false and the
// compiler drops the code behind it. In a planted build, the environment
// variable PATCHROADDB_PLANTED names the one bug that is on.
package planted

// The planted bugs. Each one sits where its name says; the simulator must
// catch each within its seed budget (pkg/sim).
const (
	// LostSync acknowledges a write before its Raft log entry is synced, so
	// a crash of a quorum loses it.
	LostSync = "lost-sync"
	// SkipRefresh lets a SERIALIZABLE transaction commit at a later
	// timestamp without checking that its reads still hold.
	SkipRefresh = "skip-refresh"
	// WrongFilter makes the vectorized engine drop rows that equal the
	// bound of a >= filter on integers.
	WrongFilter = "wrong-filter"
	// InternalError fails a DELETE of more than four rows with an internal
	// error.
	InternalError = "internal-error"
	// LostSegment makes a restore skip the last archive segment it should
	// replay.
	LostSegment = "lost-segment"
)
