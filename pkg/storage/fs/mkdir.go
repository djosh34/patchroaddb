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
// store. A parent it cannot sync fails the call: the store would not be
// durable.
//
// It creates the cleaned path: Pebble names a store's files by joining its
// directory with their names, which cleans it, so that is the directory
// the store's files go to.
func MkdirAllStoreDir(fs vfs.FS, path string, perm os.FileMode) error {
	path = filepath.Clean(path)
	if err := fs.MkdirAll(path, perm); err != nil {
		return errors.Wrapf(err, "could not create all directories: %s", path)
	}
	dirs, err := dirsToSync(fs, path)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if err := syncDir(fs, dir); err != nil {
			if oserror.IsPermission(err) {
				err = errors.WithHintf(err, "%s must be readable to make the store directory %s durable", dir, path)
			}
			return err
		}
	}
	return nil
}

// dirsToSync returns the parents of path and of each of its ancestors, up
// to the root. On disk, a relative path is first made absolute, and the
// parents along path with its symbolic links resolved are added: a link's
// entry is in the directory that holds the link, and the entry of the
// directory it points to is in that directory's own parent. An in-memory
// filesystem has no links.
func dirsToSync(fs vfs.FS, path string) ([]string, error) {
	var dirs []string
	seen := make(map[string]bool)
	add := func(path string) {
		for dir := path; ; {
			parent := fs.PathDir(dir)
			if parent == dir {
				return
			}
			if !seen[parent] {
				seen[parent] = true
				dirs = append(dirs, parent)
			}
			dir = parent
		}
	}
	if fs != vfs.Default {
		add(path)
		return dirs, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.Wrapf(err, "making %s absolute", path)
	}
	add(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, errors.Wrapf(err, "resolving %s", abs)
	}
	add(resolved)
	return dirs, nil
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
