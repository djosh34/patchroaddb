// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package builtins

import (
	"io"
	"runtime"
	"testing"

	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/stretchr/testify/require"
)

// TestDecompressIsBounded checks that decompress() refuses output larger than
// 128 MiB, for every codec.
func TestDecompressIsBounded(t *testing.T) {
	defer leaktest.AfterTest(t)()
	large := make([]byte, 128<<20+1)
	for name := range codecs {
		t.Run(name, func(t *testing.T) {
			small, err := compress([]byte("hello"), name)
			require.NoError(t, err)
			out, err := decompress(small, name)
			require.NoError(t, err)
			require.Equal(t, "hello", string(out))

			compressed, err := compress(large, name)
			require.NoError(t, err)
			_, err = decompress(compressed, name)
			require.ErrorContains(t, err, "exceeds the limit")
		})
	}
}

// TestDecompressZstdAllocationIsBounded checks that decompressing a large zstd
// output does not allocate it before the limit applies: decompress may
// allocate what io.ReadAll needs for the limit, plus at most 64 MiB. The
// io.ReadAll cost is measured, because the race detector doubles it.
func TestDecompressZstdAllocationIsBounded(t *testing.T) {
	defer leaktest.AfterTest(t)()
	compressed, err := compress(make([]byte, 512<<20), "zstd")
	require.NoError(t, err)

	readAll := totalAlloc(func() {
		_, err := io.ReadAll(io.LimitReader(zeros{}, maxDecompressedSize+1))
		require.NoError(t, err)
	})
	decompressed := totalAlloc(func() {
		_, err := decompress(compressed, "zstd")
		require.ErrorContains(t, err, "exceeds the limit")
	})
	require.Less(t, decompressed, readAll+64<<20)
}

// totalAlloc returns the bytes allocated while f runs.
func totalAlloc(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// zeros reads an endless run of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
