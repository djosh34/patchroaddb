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
// removed from it.
//
// Pebble records which MANIFEST is current, and the store's format version,
// in marker files (marker.<name>.<iteration>.<value>). To move a marker, it
// creates the new marker file, removes the old one, and only then syncs the
// directory. A filesystem may persist the removal without the creation, so a
// crash before that sync can leave no marker at all. Without the format
// version marker, Pebble reads a CURRENT file it retired long ago and fails
// on "MANIFEST-000000"; without the manifest marker, the store looks empty,
// and Pebble refuses it or creates a new store over the old files. Syncing
// the directory first makes the new marker durable before the old one goes.
//
// Pebble keeps its markers in the store directory, dir. markerSyncFS syncs
// that directory rather than one taken from the marker's path: for an
// in-memory engine dir is "", and a bare marker name's directory, ".", does
// not exist there.
type markerSyncFS struct {
	vfs.FS
	dir string
}

// Remove implements vfs.FS.
func (fs markerSyncFS) Remove(name string) error {
	if strings.HasPrefix(fs.PathBase(name), "marker.") {
		dir, err := fs.OpenDir(fs.dir)
		if err != nil {
			return err
		}
		err = dir.Sync()
		if closeErr := dir.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return errors.Wrapf(err, "syncing the directory of %s before removing it", name)
		}
	}
	return fs.FS.Remove(name)
}

// Unwrap returns the wrapped filesystem, as Pebble's own wrappers do.
func (fs markerSyncFS) Unwrap() vfs.FS {
	return fs.FS
}
