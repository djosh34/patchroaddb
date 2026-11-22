// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

//go:build sim

package rpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

// simDialOptions makes each transparent retry of a call wait a millisecond
// in the simulator (pkg/sim). gRPC retries at once, with no limit, a call
// whose attempt got no stream, as on a transport that is closing. Natively
// the transport's reader soon marks it gone, but gosim runs one goroutine
// until it blocks: the loop never blocks, so that reader never runs and
// simulated time stands still.
func simDialOptions() []grpc.DialOption {
	return []grpc.DialOption{grpc.WithStatsHandler(retryPause{})}
}

type retryPause struct{}

func (retryPause) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }

func (retryPause) HandleRPC(_ context.Context, s stats.RPCStats) {
	if b, ok := s.(*stats.Begin); ok && b.IsTransparentRetryAttempt {
		time.Sleep(time.Millisecond)
	}
}

func (retryPause) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (retryPause) HandleConn(context.Context, stats.ConnStats) {}
