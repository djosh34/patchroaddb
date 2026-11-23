// Copyright 2023 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package startup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/pkg/kv/kvpb"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgcode"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror"
	"github.com/stretchr/testify/require"
)

func TestRetryIsInterruptedByStopper(t *testing.T) {
	ctx := context.Background()
	quiesce := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		_ = RunIdempotentWithRetry(ctx, quiesce, "trying hard",
			func(ctx context.Context) error {
				// Throttle tiny bit to avoid busy loop.
				<-time.After(time.Millisecond)
				return kvpb.NewError(kvpb.NewAmbiguousResultErrorf("test error")).GoError()
			})
		close(stop)
	}()
	close(quiesce)
	select {
	case <-stop:
	case <-time.After(30 * time.Second):
		t.Fatal("test retry didn't stop after 30 sec after quiesce")
	}
}

func TestRetryOnlyAmbiguousErrors(t *testing.T) {
	ctx := context.Background()
	stop := make(chan struct{})
	go func() {
		_ = RunIdempotentWithRetry(ctx, make(chan struct{}), "trying hard",
			func(ctx context.Context) error {
				// Throttle tiny bit to avoid busy loop.
				<-time.After(time.Millisecond)
				return errors.New("non retryable")
			})
		close(stop)
	}()
	select {
	case <-stop:
	case <-time.After(30 * time.Second):
		t.Fatal("retry didn't stop on non-retryable error")
	}
}

func TestRetry(t *testing.T) {
	ctx := context.Background()
	const tries = 2
	stop := make(chan int)
	go func() {
		count := 0
		_ = RunIdempotentWithRetry(ctx, make(chan struct{}), "trying hard",
			func(ctx context.Context) error {
				count++
				if count < tries {
					return kvpb.NewError(kvpb.NewAmbiguousResultErrorf("test error")).GoError()
				}
				return nil
			})
		stop <- count
	}()
	select {
	case count := <-stop:
		require.Equal(t, tries, count, "expected number of retries before success")
	case <-time.After(30 * time.Second):
		t.Fatal("retry didn't stop on non-retryable error")
	}
}

// TestRetryAfterFlowConnectionLost checks that a startup step whose DistSQL
// flow lost its connection to another node runs again. A sim campaign found
// nodes that failed to start because a startup read hit "inbox communication
// error" once while a peer restarted.
func TestRetryAfterFlowConnectionLost(t *testing.T) {
	ctx := context.Background()
	count := 0
	err := RunIdempotentWithRetry(ctx, make(chan struct{}), "read over a flow",
		func(ctx context.Context) error {
			count++
			if count == 1 {
				return pgerror.Wrap(context.Canceled, pgcode.InternalConnectionFailure,
					"inbox communication error")
			}
			return nil
		})
	require.NoError(t, err)
	require.Equal(t, 2, count)
}
