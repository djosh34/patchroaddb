// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package sim

import (
	"context"
	"fmt"
	"time"

	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5"
)

// What the nodes and the harness share, in the simulator and in the chaos
// run alike.
const (
	sqlPort  = 26257
	s3Bucket = "backups"
	s3Region = "us-east-1"
	// The S3 credentials: the fake S3 checks none, and the chaos run starts
	// versitygw with these.
	s3Key    = "sim-access-key"
	s3Secret = "sim-secret-key"
)

// backupConfig is the --config of every node: only its backup section,
// since the nodes run insecure. Full backups every 20 s, archive segments
// every 2 s.
func backupConfig(endpoint string) string {
	return fmt.Sprintf(`backup:
  bucket: %s
  prefix: cluster
  endpoint: %s
  region: %s
  full-schedule: "@every 20s"
  archive-interval: 2s
  keep-full: 100
`, s3Bucket, endpoint, s3Region)
}

// connect opens a SQL connection to a node as root. Statements time out
// after 10 s, so that a worker does not wait for a partitioned node
// forever.
func connect(ctx context.Context, addr string) (*pgx.Conn, error) {
	return connectDB(ctx, addr, "defaultdb")
}

// connectDB is connect to the database db.
func connectDB(ctx context.Context, addr, db string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(fmt.Sprintf(
		"postgresql://root@%s:%d/%s?sslmode=disable&connect_timeout=5&statement_timeout=10000", addr, sqlPort, db))
	if err != nil {
		return nil, err
	}
	return pgx.ConnectConfig(ctx, cfg)
}

// waitSQL waits until SELECT 1 works on the node at addr.
func waitSQL(ctx context.Context, addr string, limit time.Duration) error {
	deadline := timeutil.Now().Add(limit)
	for {
		conn, err := connect(ctx, addr)
		if err == nil {
			_, err = conn.Exec(ctx, "SELECT 1")
			_ = conn.Close(ctx)
			if err == nil {
				return nil
			}
		}
		if timeutil.Now().After(deadline) {
			return errors.Wrapf(err, "%s: SQL did not work within %s", addr, limit)
		}
		time.Sleep(time.Second)
	}
}
