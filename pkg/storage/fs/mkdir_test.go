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
	"testing"

	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/errors/oserror"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMkdirAllAndSyncParents(t *testing.T) {
	defer leaktest.AfterTest(t)()

	for _, tc := range []struct {
		path   string   // a directory that exists
		sync   []string // prefix directories of `path` that were synced
		create string   // the directory to be created

		wantExist []string // directories we want to exist after a crash
		wantGone  []string // un-synced directories we expect removed after a crash
	}{{
		path:      "/",
		create:    "/a/b",
		wantExist: []string{"/", "/a", "/a/b"},
	}, {
		path:      "/a",
		sync:      []string{"/"},
		create:    "/a", // edge case, just makes sure the base dir exists
		wantExist: []string{"/", "/a"},
	}, {
		path:      "/a",
		create:    "/a", // same edge case, but we test that there is no sync
		wantExist: []string{"/"},
		wantGone:  []string{"/a"}, // "a" was created, but the root was not synced
	}, {
		path:      "/a/b",
		sync:      []string{"/"}, // "a" is not synced, so mkdir won't persist
		create:    "/a/b/c/d/",
		wantExist: []string{"/", "/a"},
		wantGone:  []string{"/a/b", "/a/b/c", "/a/b/c/d"},
	}, {
		path:      "/a/b",
		sync:      []string{"/", "/a"}, // "a" is synced, so mkdir will persist
		create:    "/a/b/c",
		wantExist: []string{"/", "/a", "/a/b", "/a/b/c"},
	}, {
		path:      "/a/b",
		sync:      []string{"/", "/a"}, // "a" is synced, so mkdir will persist
		create:    "/a/b/c/d",
		wantExist: []string{"/", "/a", "/a/b", "/a/b/c", "/a/b/c/d"},
	}, {
		path:      "/a/b/c",
		sync:      []string{"/", "/a"}, // "b" is not synced, and won't be because "c" exists
		create:    "/a/b/c/d",
		wantExist: []string{"/", "/a", "/a/b"},
		wantGone:  []string{"/a/b/c", "/a/b/c/d"},
	}, {
		path:      "a/b", // test relative paths too
		sync:      []string{"", "a"},
		create:    "a/b/c",
		wantExist: []string{"a", "a/b", "a/b/c"},
	}, {
		path:      "a/b",
		sync:      []string{""},
		create:    "a/b/c",
		wantExist: []string{"a"},
		wantGone:  []string{"a/b", "a/b/c"},
	}, {
		path:      "../a/b",
		sync:      []string{"", "..", "../a"},
		create:    "../a/b/c",
		wantExist: []string{"../a/b/c"},
	}, {
		path:      "../a/b",
		sync:      []string{"", ".."}, // "a" not synced, the dirs will be lost
		create:    "../a/b/c",
		wantExist: []string{".."},
		wantGone:  []string{"../a/b/c"},
	}} {
		t.Run("", func(t *testing.T) {
			fs := vfs.NewStrictMem()
			require.NoError(t, fs.MkdirAll(tc.path, os.ModePerm))
			for _, dir := range tc.sync {
				handle, err := fs.OpenDir(dir)
				require.NoError(t, err)
				require.NoError(t, handle.Sync())
				require.NoError(t, handle.Close())
			}
			require.NoError(t, MkdirAllAndSyncParents(fs, tc.create, os.ModePerm))

			assertExistence := func(t *testing.T, dirs []string, exist bool) {
				t.Helper()
				for _, dir := range dirs {
					handle, err := fs.OpenDir(dir)
					if exist {
						require.NoError(t, err, dir)
						require.NoError(t, handle.Close(), dir)
					} else {
						require.True(t, oserror.IsNotExist(err), dir)
					}
				}
			}

			// Before crash, all the relevant directories must exist.
			assertExistence(t, tc.wantExist, true)
			assertExistence(t, tc.wantGone, true)
			// After crash and resetting to the synced state, wantExist directories
			// must exist, and wantGone are lost.
			fs.ResetToSyncedState()
			assertExistence(t, tc.wantExist, true)
			assertExistence(t, tc.wantGone, false)
		})
	}
}

func TestMkdirAllAndSyncParentsErrors(t *testing.T) {
	defer leaktest.AfterTest(t)()

	t.Run("empty", func(t *testing.T) {
		fs := vfs.NewMem()
		for _, path := range []string{".", "./a", ".."} {
			require.ErrorContains(t, MkdirAllAndSyncParents(fs, path, os.ModePerm),
				"topmost dir does not exist", path)
		}
		// The root exists in an empty MemFS.
		require.NoError(t, MkdirAllAndSyncParents(fs, "/", os.ModePerm))
		// TODO(pavelkalinnikov): find a way to remove "/", and exercise the missing
		// root error for absolute paths.
		// For now, removing "/" does not succeed in MemFS.
		assert.ErrorContains(t, fs.Remove("/"), "empty file name")
	})

	t.Run("not-a-directory", func(t *testing.T) {
		fs := vfs.NewMem()
		require.NoError(t, MkdirAllAndSyncParents(fs, "/a", os.ModePerm))

		// Write a file, and try to trick mkdir into thinking that it's a directory.
		f, err := fs.Create("/a/file")
		require.NoError(t, err)
		require.NoError(t, f.Close())

		for _, path := range []string{"/a/file", "/a/file/sub"} {
			require.ErrorContains(t, MkdirAllAndSyncParents(fs, path, os.ModePerm), "not a directory")
		}
	})
}

// failOpenDir fails OpenDir of dir with err, at most times times.
type failOpenDir struct {
	*vfs.MemFS
	dir   string
	err   error
	times int
}

func (fs *failOpenDir) OpenDir(name string) (vfs.File, error) {
	if name == fs.dir && fs.times > 0 {
		fs.times--
		return nil, fs.err
	}
	return fs.MemFS.OpenDir(name)
}

// TestMkdirAllStoreDirSurvivesCrash checks that MkdirAllStoreDir makes the
// directory durable however its ancestors came to be: created by this
// call, left unsynced by another, or by an earlier call that failed to
// sync them.
func TestMkdirAllStoreDirSurvivesCrash(t *testing.T) {
	defer leaktest.AfterTest(t)()

	t.Run("new", func(t *testing.T) {
		fs := vfs.NewStrictMem()
		require.NoError(t, MkdirAllStoreDir(fs, "/a/b/c", os.ModePerm))
		fs.ResetToSyncedState()
		_, err := fs.Stat("/a/b/c")
		require.NoError(t, err)
	})
	t.Run("unsynced-ancestors", func(t *testing.T) {
		fs := vfs.NewStrictMem()
		require.NoError(t, fs.MkdirAll("/a/b", os.ModePerm))
		require.NoError(t, MkdirAllStoreDir(fs, "/a/b/c", os.ModePerm))
		fs.ResetToSyncedState()
		_, err := fs.Stat("/a/b/c")
		require.NoError(t, err)
	})
	t.Run("retry-after-sync-error", func(t *testing.T) {
		fs := &failOpenDir{MemFS: vfs.NewStrictMem(), dir: "/", err: errors.New("injected EIO"), times: 1}
		require.Error(t, MkdirAllStoreDir(fs, "/a/b", os.ModePerm))
		require.NoError(t, MkdirAllStoreDir(fs, "/a/b", os.ModePerm))
		fs.ResetToSyncedState()
		_, err := fs.Stat("/a/b")
		require.NoError(t, err)
	})
}

// TestMkdirAllStoreDirRefusesUnreadableParent checks that a parent the node
// may not open for its sync fails the call: the store directory in it would
// not be durable.
func TestMkdirAllStoreDirRefusesUnreadableParent(t *testing.T) {
	defer leaktest.AfterTest(t)()

	mem := vfs.NewStrictMem()
	require.NoError(t, mem.MkdirAll("/home", os.ModePerm))
	fs := &failOpenDir{MemFS: mem, dir: "/home", err: os.ErrPermission, times: 1 << 30}
	err := MkdirAllStoreDir(fs, "/home/data", os.ModePerm)
	require.True(t, oserror.IsPermission(err), "%v", err)
}

// TestMkdirAllStoreDirResolvesLinks checks that on disk the directories
// MkdirAllStoreDir syncs are those of the directory the system creates: a
// path through a symbolic link, with a trailing separator or with ".."
// after the link, names the link target's directories.
func TestMkdirAllStoreDirResolvesLinks(t *testing.T) {
	defer leaktest.AfterTest(t)()

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "disk", "sub"), 0755))
	link := filepath.Join(tmp, "link")
	require.NoError(t, os.Symlink(filepath.Join(tmp, "disk", "sub"), link))

	for path, want := range map[string]string{
		filepath.Join(link, "store") + "/":     filepath.Join(tmp, "disk", "sub", "store"),
		link + "/../store2":                    filepath.Join(tmp, "disk", "store2"),
		filepath.Join(tmp, ".", "plain") + "/": filepath.Join(tmp, "plain"),
	} {
		require.NoError(t, MkdirAllStoreDir(vfs.Default, path, 0755), path)
		got, err := createdDir(vfs.Default, path)
		require.NoError(t, err)
		require.Equal(t, want, got, path)
		_, err = os.Stat(want)
		require.NoError(t, err, path)
	}
}
