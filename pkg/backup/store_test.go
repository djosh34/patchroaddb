// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/cockroachdb/cockroach/pkg/backup/backuptest"
	"github.com/cockroachdb/cockroach/pkg/sim/fakes3"
	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/require"
)

// TestS3Contract checks every S3 call that Store makes against a real S3
// server: what the backup code relies on.
func TestS3Contract(t *testing.T) {
	defer leaktest.AfterTest(t)()
	bucket, endpoint := backuptest.NewBucket(t)
	testS3Contract(t, bucket, endpoint)
}

// TestS3ContractFake runs the same checks against the simulator's fake S3,
// so that the simulator's S3 behaves as a real one where the backup code
// depends on it.
func TestS3ContractFake(t *testing.T) {
	defer leaktest.AfterTest(t)()
	t.Setenv("AWS_ACCESS_KEY_ID", "fake")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fake")
	fake := fakes3.New(mathrand.New(mathrand.NewPCG(1, 2)))
	fake.CreateBucket("fake")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	testS3Contract(t, "fake", srv.URL)
}

func testS3Contract(t *testing.T, bucket, endpoint string) {
	ctx := context.Background()
	s, err := OpenStore(ctx, Repository{Bucket: bucket, Prefix: "repo", Endpoint: endpoint, Region: backuptest.Region})
	require.NoError(t, err)

	get := func(name string) ([]byte, error) {
		r, err := s.Get(ctx, name)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	random := func(n int) []byte {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		return b
	}

	t.Run("PutObject and GetObject", func(t *testing.T) {
		require.NoError(t, s.Put(ctx, "small", strings.NewReader("hello")))
		b, err := get("small")
		require.NoError(t, err)
		require.Equal(t, "hello", string(b))
	})

	t.Run("an empty object", func(t *testing.T) {
		require.NoError(t, s.Put(ctx, "empty", bytes.NewReader(nil)))
		b, err := get("empty")
		require.NoError(t, err)
		require.Empty(t, b)
	})

	t.Run("overwrite", func(t *testing.T) {
		require.NoError(t, s.Put(ctx, "over", strings.NewReader("first")))
		require.NoError(t, s.Put(ctx, "over", strings.NewReader("second")))
		b, err := get("over")
		require.NoError(t, err)
		require.Equal(t, "second", string(b))
	})

	t.Run("multipart upload", func(t *testing.T) {
		// Three parts: two full ones and a short last one.
		want := random(2*partSize + 12345)
		require.NoError(t, s.Put(ctx, "large", bytes.NewReader(want)))
		b, err := get("large")
		require.NoError(t, err)
		require.True(t, bytes.Equal(want, b), "the object differs from what was uploaded")
	})

	t.Run("a failed multipart upload leaves nothing", func(t *testing.T) {
		require.NoError(t, s.Put(ctx, "partial", strings.NewReader("old")))
		body := io.MultiReader(bytes.NewReader(random(2*partSize)), errReader{})
		require.Error(t, s.Put(ctx, "partial", body))
		b, err := get("partial")
		require.NoError(t, err)
		require.Equal(t, "old", string(b), "a failed upload changed the object")
		// AbortMultipartUpload removed the parts.
		out, err := s.client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
		require.NoError(t, err)
		require.Empty(t, out.Uploads)
	})

	t.Run("create only if absent", func(t *testing.T) {
		for _, size := range []int{5, 2*partSize + 1} {
			name := fmt.Sprintf("once-%d", size)
			first, second := random(size), random(size)
			require.NoError(t, s.Create(ctx, name, bytes.NewReader(first)))
			err := s.Create(ctx, name, bytes.NewReader(second))
			require.True(t, errors.Is(err, ErrExists), "size %d: %v", size, err)
			b, err := get(name)
			require.NoError(t, err)
			require.True(t, bytes.Equal(first, b), "size %d: the second create changed the object", size)
		}
	})

	t.Run("GetObject of a missing object", func(t *testing.T) {
		_, err := get("missing")
		require.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})

	t.Run("ListObjectsV2 order and paging", func(t *testing.T) {
		for _, n := range []string{"d/b", "d/a", "d/c/x", "d/aa", "d/B", "d0/z", "e/z"} {
			require.NoError(t, s.Put(ctx, n, strings.NewReader(n)))
		}
		want := []string{"B", "a", "aa", "b", "c/x"}
		for _, page := range []int32{0, 1, 2} {
			s.listPageSize = page
			names, err := s.List(ctx, "d")
			require.NoError(t, err)
			require.Equal(t, want, names, "page size %d", page)
		}
		s.listPageSize = 0
		names, err := s.List(ctx, "nothing")
		require.NoError(t, err)
		require.Empty(t, names)
	})

	t.Run("DeleteObject", func(t *testing.T) {
		require.NoError(t, s.Put(ctx, "gone", strings.NewReader("x")))
		require.NoError(t, s.Delete(ctx, "gone"))
		_, err := get("gone")
		require.True(t, errors.Is(err, ErrNotFound), "%v", err)
		// Deleting a missing object succeeds.
		require.NoError(t, s.Delete(ctx, "gone"))
	})

	t.Run("a missing bucket", func(t *testing.T) {
		m, err := OpenStore(ctx, Repository{Bucket: bucket + "-missing", Endpoint: endpoint, Region: backuptest.Region})
		require.NoError(t, err)
		code := func(err error) string {
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) {
				return ""
			}
			return apiErr.ErrorCode()
		}
		require.Equal(t, "NoSuchBucket", code(m.Put(ctx, "x", strings.NewReader("x"))))
		_, err = m.Get(ctx, "x")
		require.Equal(t, "NoSuchBucket", code(err))
		_, err = m.List(ctx, "x")
		require.Equal(t, "NoSuchBucket", code(err))
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the source failed") }
