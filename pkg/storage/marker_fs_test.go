// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package storage

import (
	"strings"
	"testing"

	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/cockroachdb/pebble/vfs/atomicfs"
	"github.com/stretchr/testify/require"
)

// crashOnMarkerRemoveFS crashes the first time a marker file is removed,
// and persists that removal, but nothing else that was not synced: what a
// filesystem that reorders directory updates may leave.
type crashOnMarkerRemoveFS struct {
	*vfs.MemFS
	crashed bool
}

func (fs *crashOnMarkerRemoveFS) Remove(name string) error {
	if fs.crashed || !strings.HasPrefix(fs.PathBase(name), "marker.") {
		return fs.MemFS.Remove(name)
	}
	fs.crashed = true
	fs.ResetToSyncedState()
	if err := fs.MemFS.Remove(name); err != nil {
		return err
	}
	return errors.New("crashed")
}

// TestMarkerSurvivesCrashDuringMove checks that a crash while Pebble moves
// an atomic marker leaves the old or the new marker, never none.
func TestMarkerSurvivesCrashDuringMove(t *testing.T) {
	defer leaktest.AfterTest(t)()

	mem := &crashOnMarkerRemoveFS{MemFS: vfs.NewStrictMem()}
	fs := markerSyncFS{FS: mem, dir: "/"}
	m, _, err := atomicfs.LocateMarker(fs, "/", "format-version")
	require.NoError(t, err)
	defer func() { require.NoError(t, m.Close()) }()

	require.NoError(t, m.Move("001"))
	// The second move removes the first marker, and crashes there.
	require.NoError(t, m.Move("002"))
	require.True(t, mem.crashed)

	value, err := atomicfs.ReadMarker(mem, "/", "format-version")
	require.NoError(t, err)
	require.Equal(t, "002", value)
}

// TestMarkerMoveInMemoryRoot checks that moving a marker in an in-memory
// engine, whose directory is "", removes the old marker.
func TestMarkerMoveInMemoryRoot(t *testing.T) {
	defer leaktest.AfterTest(t)()

	mem := vfs.NewMem()
	m, _, err := atomicfs.LocateMarker(markerSyncFS{FS: mem, dir: ""}, "", "format-version")
	require.NoError(t, err)
	defer func() { require.NoError(t, m.Close()) }()
	require.NoError(t, m.Move("001"))
	require.NoError(t, m.Move("002"))

	files, err := mem.List("")
	require.NoError(t, err)
	require.Equal(t, []string{"marker.format-version.000002.002"}, files)
}
