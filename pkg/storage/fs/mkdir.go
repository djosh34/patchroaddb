// Copyright 2017 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package fs

import (
	"os"
	"path/filepath"

	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/errors/oserror"
	"github.com/cockroachdb/pebble/vfs"
)

// MkdirAllAndSyncParents creates the given directory and all its missing
// parents if any. For every newly created directly, it syncs the corresponding
// parent directory. The directories are created using the provided permissions
// mask, with the same semantics as in os.MkdirAll.
//
// For example, if path is "/x/y/z", and "/x" previously existed, then this func
// creates "/x/y" and "/x/y/z", and syncs directories "/x" and "/x/y".
//
// TODO(pavelkalinnikov): this does not work well with paths containing . and ..
// elements inside the data-dir directory. We don't construct the path this way
// though, right now any non-canonical part of the path would be only in the
// <data-dir> path.
//
// TODO(pavelkalinnikov): have a type-safe canonical path type which can be
// iterated without thinking about . and .. placeholders.
func MkdirAllAndSyncParents(fs vfs.FS, path string, perm os.FileMode) error {
	// Find the lowest existing directory in the hierarchy.
	var exists string
	for dir, parent := path, ""; ; dir = parent {
		if _, err := fs.Stat(dir); err == nil {
			exists = dir
			break
		} else if !oserror.IsNotExist(err) {
			return errors.Wrapf(err, "could not get dir info: %s", dir)
		}
		parent = fs.PathDir(dir)
		// NB: not checking against the separator, to be platform-agnostic.
		if dir == "." || parent == dir { // reached the topmost dir or the root
			return errors.Newf("topmost dir does not exist: %s", dir)
		}
	}

	// Create the destination directory and any of its missing parents.
	if err := fs.MkdirAll(path, perm); err != nil {
		return errors.Wrapf(err, "could not create all directories: %s", path)
	}

	// Sync parent directories up to the lowest existing ancestor, included.
	for dir, parent := path, ""; dir != exists; dir = parent {
		parent = fs.PathDir(dir)
		if handle, err := fs.OpenDir(parent); err != nil {
			return errors.Wrapf(err, "could not open parent dir: %s", parent)
		} else if err := handle.Sync(); err != nil {
			_ = handle.Close()
			return errors.Wrapf(err, "could not sync parent dir: %s", parent)
		} else if err := handle.Close(); err != nil {
			return errors.Wrapf(err, "could not close parent dir: %s", parent)
		}
	}

	return nil
}

// MkdirAllStoreDir creates a store directory, or a directory in it, on a
// node's start, and makes it durable: it syncs the parent of the directory
// and of each of its ancestors, up to the root. Whether an ancestor's entry
// is durable cannot be told from its being there: an earlier start may have
// created it and stopped before its sync, and a crash would then lose the
// store. A parent it cannot sync, also for lack of read permission, fails
// the call: the store would not be durable.
//
// The path must be clean, as the CLI makes store paths: with "..", "." or
// a trailing separator, the directories it syncs would not be the ones
// MkdirAll creates.
func MkdirAllStoreDir(fs vfs.FS, path string, perm os.FileMode) error {
	if filepath.Clean(path) != path {
		return errors.Newf("store directory %q is not a clean path", path)
	}
	if err := fs.MkdirAll(path, perm); err != nil {
		return errors.Wrapf(err, "could not create all directories: %s", path)
	}
	for dir := path; ; {
		parent := fs.PathDir(dir)
		if parent == dir {
			return nil
		}
		if err := syncDir(fs, parent); err != nil {
			return errors.WithHintf(err,
				"%s must be readable to make the store directory %s durable", parent, path)
		}
		dir = parent
	}
}

// syncDir syncs the directory dir.
func syncDir(fs vfs.FS, dir string) error {
	handle, err := fs.OpenDir(dir)
	if err != nil {
		return errors.Wrapf(err, "could not open dir: %s", dir)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return errors.Wrapf(err, "could not sync dir: %s", dir)
	}
	return errors.Wrapf(handle.Close(), "could not close dir: %s", dir)
}
