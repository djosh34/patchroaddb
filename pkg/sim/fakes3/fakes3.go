// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

// Package fakes3 is an S3 server that keeps its objects in memory, for the
// simulator: real S3 is not available inside gosim. It serves the calls that
// pkg/backup's Store makes, path-style, and checks no signatures. The S3
// contract tests of pkg/backup run against it and against versitygw, so
// both behave alike where the backup code depends on it.
//
// Faults makes some requests fail or stall, from a random source the caller
// gives: inside the simulator, the seed decides which.
package fakes3

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/cockroach/pkg/util/syncutil"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
)

// Server is an in-memory S3 server. Its zero value is not usable; use New.
type Server struct {
	mu      syncutil.Mutex
	buckets map[string]*bucket
	uploads map[string]*upload
	nextID  int
	faults  Faults
	rng     *rand.Rand
}

type bucket struct {
	objects map[string]object
}

type object struct {
	data     []byte
	etag     string
	modified time.Time
}

type upload struct {
	bucket, key string
	parts       map[int]object
}

// Faults says how often a request fails or stalls. A failed request gets
// an InternalError or a SlowDown answer and changes nothing; a stalled one
// waits Stall before it is served.
type Faults struct {
	// ErrorRate is the share of requests that fail, from 0 to 1.
	ErrorRate float64
	// StallRate is the share of requests that wait Stall first.
	StallRate float64
	Stall     time.Duration
}

// New returns an empty server. rng decides the faults.
func New(rng *rand.Rand) *Server {
	return &Server{
		buckets: make(map[string]*bucket),
		uploads: make(map[string]*upload),
		rng:     rng,
	}
}

// CreateBucket creates an empty bucket, if it does not exist yet.
func (s *Server) CreateBucket(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets[name] == nil {
		s.buckets[name] = &bucket{objects: make(map[string]object)}
	}
}

// SetFaults sets the faults of later requests.
func (s *Server) SetFaults(f Faults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = f
}

// Keys returns the names of the objects in a bucket, in order.
func (s *Server) Keys(bucketName string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buckets[bucketName]
	if b == nil {
		return nil
	}
	keys := make([]string, 0, len(b.objects))
	for k := range b.objects {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// s3Error is the body of an S3 error answer.
type s3Error struct {
	XMLName    xml.Name `xml:"Error"`
	Code       string   `xml:"Code"`
	Message    string   `xml:"Message"`
	BucketName string   `xml:"BucketName,omitempty"`
	Key        string   `xml:"Key,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(s3Error{Code: code, Message: msg})
}

func writeXML(w http.ResponseWriter, v any) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(v); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

// fault draws whether this request fails or stalls.
func (s *Server) fault() (fail bool, stall time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.faults.StallRate > 0 && s.rng.Float64() < s.faults.StallRate {
		stall = s.faults.Stall
	}
	return s.faults.ErrorRate > 0 && s.rng.Float64() < s.faults.ErrorRate, stall
}

// ServeHTTP serves one S3 request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "IncompleteBody", err.Error())
		return
	}
	fail, stall := s.fault()
	if stall > 0 {
		select {
		case <-time.After(stall):
		case <-r.Context().Done():
			return
		}
	}
	if fail {
		if s.rngIntn(2) == 0 {
			writeError(w, http.StatusInternalServerError, "InternalError", "injected fault")
		} else {
			writeError(w, http.StatusServiceUnavailable, "SlowDown", "injected fault")
		}
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	bucketName, key, _ := strings.Cut(path, "/")
	q := r.URL.Query()

	s.mu.Lock()
	defer s.mu.Unlock()

	if bucketName == "" {
		writeError(w, http.StatusNotImplemented, "NotImplemented", "listing buckets")
		return
	}
	b := s.buckets[bucketName]
	if key == "" {
		switch {
		case r.Method == http.MethodPut:
			if b == nil {
				s.buckets[bucketName] = &bucket{objects: make(map[string]object)}
			}
			w.WriteHeader(http.StatusOK)
		case b == nil:
			writeError(w, http.StatusNotFound, "NoSuchBucket", "the bucket does not exist")
		case r.Method == http.MethodGet && q.Has("uploads"):
			s.listUploads(w, bucketName)
		case r.Method == http.MethodGet && q.Get("list-type") == "2":
			s.listObjects(w, bucketName, b, q)
		default:
			writeError(w, http.StatusNotImplemented, "NotImplemented", r.Method+" on a bucket")
		}
		return
	}
	if b == nil {
		writeError(w, http.StatusNotFound, "NoSuchBucket", "the bucket does not exist")
		return
	}

	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		s.nextID++
		id := fmt.Sprintf("upload-%d", s.nextID)
		s.uploads[id] = &upload{bucket: bucketName, key: key, parts: make(map[int]object)}
		writeXML(w, initiateResult{Bucket: bucketName, Key: key, UploadID: id})
	case r.Method == http.MethodPut && q.Has("uploadId"):
		u := s.uploads[q.Get("uploadId")]
		n, err := strconv.Atoi(q.Get("partNumber"))
		if u == nil || u.bucket != bucketName || u.key != key {
			writeError(w, http.StatusNotFound, "NoSuchUpload", "the upload does not exist")
			return
		}
		if err != nil || n < 1 || n > 10000 {
			writeError(w, http.StatusBadRequest, "InvalidArgument", "bad part number")
			return
		}
		o := newObject(body)
		u.parts[n] = o
		w.Header().Set("ETag", o.etag)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		s.complete(w, r, b, bucketName, key, q.Get("uploadId"), body)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		u := s.uploads[q.Get("uploadId")]
		if u == nil || u.bucket != bucketName || u.key != key {
			writeError(w, http.StatusNotFound, "NoSuchUpload", "the upload does not exist")
			return
		}
		delete(s.uploads, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		if _, exists := b.objects[key]; exists && r.Header.Get("If-None-Match") == "*" {
			writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "the object exists")
			return
		}
		o := newObject(body)
		b.objects[key] = o
		w.Header().Set("ETag", o.etag)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		o, ok := b.objects[key]
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeError(w, http.StatusNotFound, "NoSuchKey", "the object does not exist")
			return
		}
		data, status := o.data, http.StatusOK
		if rng := r.Header.Get("Range"); rng != "" && r.Method == http.MethodGet {
			start, end, ok := parseRange(rng, len(o.data))
			if !ok {
				writeError(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "bad range "+rng)
				return
			}
			data, status = o.data[start:end], http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(o.data)))
		}
		w.Header().Set("ETag", o.etag)
		w.Header().Set("Last-Modified", o.modified.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(status)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	case r.Method == http.MethodDelete:
		delete(b.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusNotImplemented, "NotImplemented", r.Method+" on an object")
	}
}

func (s *Server) rngIntn(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.IntN(n)
}

// parseRange parses a Range header of one range, "bytes=a-b" or "bytes=a-",
// into the half-open interval [start, end) of an object of size n.
func parseRange(h string, n int) (start, end int, ok bool) {
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	first, last, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.Atoi(first)
	if err != nil || start < 0 || start >= n {
		return 0, 0, false
	}
	end = n
	if last != "" {
		l, err := strconv.Atoi(last)
		if err != nil || l < start {
			return 0, 0, false
		}
		// min before adding one: l may be the largest int.
		end = min(l, n-1) + 1
	}
	return start, end, true
}

func newObject(data []byte) object {
	sum := md5.Sum(data)
	return object{data: data, etag: `"` + hex.EncodeToString(sum[:]) + `"`, modified: timeutil.Now()}
}

type initiateResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeRequest struct {
	Parts []struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	} `xml:"Part"`
}

type completeResult struct {
	XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
	Bucket  string   `xml:"Bucket"`
	Key     string   `xml:"Key"`
	ETag    string   `xml:"ETag"`
}

// complete joins the listed parts into the object, all or nothing.
func (s *Server) complete(
	w http.ResponseWriter, r *http.Request, b *bucket, bucketName, key, id string, body []byte,
) {
	u := s.uploads[id]
	if u == nil || u.bucket != bucketName || u.key != key {
		writeError(w, http.StatusNotFound, "NoSuchUpload", "the upload does not exist")
		return
	}
	var req completeRequest
	if err := xml.Unmarshal(body, &req); err != nil || len(req.Parts) == 0 {
		writeError(w, http.StatusBadRequest, "MalformedXML", "bad part list")
		return
	}
	var data []byte
	last := 0
	for _, p := range req.Parts {
		part, ok := u.parts[p.PartNumber]
		if !ok || part.etag != p.ETag {
			writeError(w, http.StatusBadRequest, "InvalidPart", "a listed part was not uploaded")
			return
		}
		if p.PartNumber <= last {
			writeError(w, http.StatusBadRequest, "InvalidPartOrder", "parts out of order")
			return
		}
		last = p.PartNumber
		data = append(data, part.data...)
	}
	if _, exists := b.objects[key]; exists && r.Header.Get("If-None-Match") == "*" {
		writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "the object exists")
		return
	}
	o := newObject(data)
	b.objects[key] = o
	delete(s.uploads, id)
	writeXML(w, completeResult{Bucket: bucketName, Key: key, ETag: o.etag})
}

type listUploadsResult struct {
	XMLName xml.Name `xml:"ListMultipartUploadsResult"`
	Bucket  string   `xml:"Bucket"`
	Uploads []struct {
		Key      string `xml:"Key"`
		UploadID string `xml:"UploadId"`
	} `xml:"Upload"`
}

func (s *Server) listUploads(w http.ResponseWriter, bucketName string) {
	res := listUploadsResult{Bucket: bucketName}
	ids := make([]string, 0, len(s.uploads))
	for id, u := range s.uploads {
		if u.bucket == bucketName {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		res.Uploads = append(res.Uploads, struct {
			Key      string `xml:"Key"`
			UploadID string `xml:"UploadId"`
		}{Key: s.uploads[id].key, UploadID: id})
	}
	writeXML(w, res)
}

type listResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	Name                  string   `xml:"Name"`
	Prefix                string   `xml:"Prefix"`
	KeyCount              int      `xml:"KeyCount"`
	MaxKeys               int      `xml:"MaxKeys"`
	IsTruncated           bool     `xml:"IsTruncated"`
	ContinuationToken     string   `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string   `xml:"NextContinuationToken,omitempty"`
	Contents              []struct {
		Key          string `xml:"Key"`
		Size         int    `xml:"Size"`
		ETag         string `xml:"ETag"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
}

// listObjects lists the keys with the prefix in byte order, in pages. The
// continuation token is the last key of the previous page.
func (s *Server) listObjects(
	w http.ResponseWriter, bucketName string, b *bucket, q map[string][]string,
) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	prefix := get("prefix")
	maxKeys := 1000
	if m := get("max-keys"); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "InvalidArgument", "bad max-keys")
			return
		}
		maxKeys = min(n, 1000)
	}
	after := get("continuation-token")
	if after == "" {
		after = get("start-after")
	}
	var keys []string
	for k := range b.objects {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	res := listResult{Name: bucketName, Prefix: prefix, MaxKeys: maxKeys, ContinuationToken: get("continuation-token")}
	if maxKeys == 0 {
		// An empty page, with no token to continue from.
		keys = nil
	}
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
		res.IsTruncated = true
		res.NextContinuationToken = keys[len(keys)-1]
	}
	for _, k := range keys {
		o := b.objects[k]
		res.Contents = append(res.Contents, struct {
			Key          string `xml:"Key"`
			Size         int    `xml:"Size"`
			ETag         string `xml:"ETag"`
			LastModified string `xml:"LastModified"`
		}{Key: k, Size: len(o.data), ETag: o.etag, LastModified: o.modified.UTC().Format(time.RFC3339)})
	}
	res.KeyCount = len(res.Contents)
	writeXML(w, res)
}

// readBody reads a request's body. The AWS SDK may send it in aws-chunked
// encoding, with each chunk's size before it and trailing headers after.
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") &&
		!strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		return body, nil
	}
	var out []byte
	br := bufio.NewReader(bytes.NewReader(body))
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("aws-chunked: %w", err)
		}
		sizeHex, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, err := strconv.ParseInt(sizeHex, 16, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("aws-chunked: bad chunk size %q", line)
		}
		if size == 0 {
			return out, nil
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(br, chunk); err != nil {
			return nil, fmt.Errorf("aws-chunked: %w", err)
		}
		out = append(out, chunk...)
		if _, err := br.ReadString('\n'); err != nil {
			return nil, fmt.Errorf("aws-chunked: %w", err)
		}
	}
}
