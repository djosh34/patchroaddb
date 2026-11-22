// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package fakes3

import (
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestEdges covers requests at the limits: a page of no keys, and a range
// that ends at the largest int.
func TestEdges(t *testing.T) {
	s := New(rand.New(rand.NewPCG(1, 2)))
	s.CreateBucket("b")
	srv := httptest.NewServer(s)
	defer srv.Close()
	do := func(method, path, body string, header http.Header) (int, string) {
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range header {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	if code, _ := do(http.MethodPut, "/b/k", "hello", nil); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	code, body := do(http.MethodGet, "/b?list-type=2&max-keys=0", "", nil)
	if code != http.StatusOK || strings.Contains(body, "<Key>") || strings.Contains(body, "NextContinuationToken") {
		t.Errorf("list with max-keys=0: %d %s", code, body)
	}
	rng := http.Header{"Range": {"bytes=1-" + strconv.Itoa(math.MaxInt)}}
	if code, body := do(http.MethodGet, "/b/k", "", rng); code != http.StatusPartialContent || body != "ello" {
		t.Errorf("range to the largest int: %d %q", code, body)
	}
}
