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

	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/pebble/vfs"
)

// markerSyncFS syncs the store directory before an atomic marker file is
// created in it or removed from it.
//
// Pebble records which MANIFEST is current, and the store's format version,
// in marker files (marker.<name>.<iteration>.<value>). To move a marker, it
// creates the new marker file, removes the old one, and only then syncs the
// directory. A filesystem may persist those directory updates in any order,
// so a crash before that sync can leave:
//   - the new marker without the file it names, created just before it,
//     such as a new MANIFEST: Pebble then fails on the missing file;
//   - no marker at all, the old one removed and the new one not created.
//     Without the format version marker, Pebble reads a CURRENT file it
//     retired long ago and fails on "MANIFEST-000000"; without the manifest
//     marker, the store looks empty, and Pebble refuses it or creates a new
//     store over the old files.
//
// Syncing the directory before the new marker is created makes what it
// names durable first, and syncing it before the old marker is removed
// makes the new marker durable before the old one goes.
//
// Pebble keeps its markers in the store directory, dir. markerSyncFS syncs
// that directory rather than one taken from the marker's path: for an
// in-memory engine dir is "", and a bare marker name's directory, ".", does
// not exist there.
type markerSyncFS struct {
	vfs.FS
	dir string
}

// Create implements vfs.FS.
func (fs markerSyncFS) Create(name string) (vfs.File, error) {
	if err := fs.syncBeforeMarker(name, "creating"); err != nil {
		return nil, err
	}
	return fs.FS.Create(name)
}

// Remove implements vfs.FS.
func (fs markerSyncFS) Remove(name string) error {
	if err := fs.syncBeforeMarker(name, "removing"); err != nil {
		return err
	}
	return fs.FS.Remove(name)
}

// syncBeforeMarker syncs the store directory if name is a marker file.
func (fs markerSyncFS) syncBeforeMarker(name, what string) error {
	if !strings.HasPrefix(fs.PathBase(name), "marker.") {
		return nil
	}
	dir, err := fs.OpenDir(fs.dir)
	if err != nil {
		return err
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	return errors.Wrapf(err, "syncing the directory of %s before %s it", name, what)
}

// Unwrap returns the wrapped filesystem, as Pebble's own wrappers do.
func (fs markerSyncFS) Unwrap() vfs.FS {
	return fs.FS
}
