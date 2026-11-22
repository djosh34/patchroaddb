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
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/cockroach/pkg/backup"
	"github.com/cockroachdb/cockroach/pkg/util/hlc"
	"github.com/cockroachdb/cockroach/pkg/util/syncutil"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5"
)

// env is where a scenario runs: simulated machines under gosim
// (cluster.go), or real processes in the nightly chaos run (chaos.go).
// Nodes are numbered from 0.
type env interface {
	// addrs are the SQL hosts of the nodes.
	addrs() []string
	// s3Endpoint is the URL of the S3 server.
	s3Endpoint() string
	// crash stops node i abruptly, losing what it has not synced, and
	// reports false, doing nothing, if the node is down already.
	crash(i int) bool
	// restart starts a node that crash stopped. With partial, a random
	// part of its unsynced writes reaches the disk, where supported.
	restart(i int, partial bool)
	// partition cuts node i off from the other nodes, or ends that.
	partition(i int, on bool)
	// delay sets the network delay between nodes i and j; 0 restores the
	// base latency between them.
	delay(i, j int, d time.Duration)
	// s3Faults makes S3 fail and stall requests, or stop doing so.
	s3Faults(on bool)
	// syncFaults makes fsync crash node i at random, or stop doing so; it
	// reports false where that is not supported.
	syncFaults(i int, on bool) bool
	// startRestoreNode starts a new single-node cluster for a restore and
	// returns its SQL host.
	startRestoreNode(ctx context.Context) (string, error)
	// failed lists the nodes that stopped on their own.
	failed() []string
}

// scenario is one seed's run: the generated schema and workload, the
// faults, and what the checks need to know.
type scenario struct {
	rng    *rand.Rand
	env    env
	tables []table

	// Sizes, from the seed.
	workers  int
	accounts int
	keys     int
	runFor   time.Duration
	// think is a worker's mean pause between two statements. Without it
	// the workers send tens of thousands of statements a seed, which costs
	// minutes of CPU, where a few hundred already contend. With runFor, it
	// keeps a seed near 10 CPU-seconds.
	think time.Duration

	mu       syncutil.Mutex
	failures []string
	ledger   map[int]*ledgerKey
	// pitr is a time during the workload that the PITR check restores
	// to; zero until the workload records it.
	pitr hlc.Timestamp
	// queries are generated SELECTs; the check compares their plans.
	queries []query
	// stats count what the workload did, for the log.
	stats map[string]int
	// phase, if set, is called as each phase of run ends, with its name,
	// to measure what the phase cost.
	phase func(name string)
}

const bankTotal = 1000

// newScenario draws a scenario's sizes and schema from rng.
func newScenario(rng *rand.Rand, e env) *scenario {
	s := &scenario{
		rng:      rng,
		env:      e,
		tables:   genSchema(rng),
		workers:  2 + rng.IntN(4),
		accounts: 3 + rng.IntN(5),
		runFor:   time.Duration(10+rng.IntN(15)) * time.Second,
		think:    time.Duration(100+rng.IntN(300)) * time.Millisecond,
		ledger:   make(map[int]*ledgerKey),
		stats:    make(map[string]int),
	}
	s.keys = s.workers * (2 + rng.IntN(8))
	return s
}

// run sets up the schema and data, runs the workload with faults for
// s.runFor, heals the cluster and checks it. It returns an error when the
// run could not go on; the failures of the checks are in s.failures.
func (s *scenario) run(ctx context.Context) error {
	if err := s.setup(ctx); err != nil {
		return errors.Wrap(err, "setup")
	}
	s.endPhase("setup")
	newRNG := func() *rand.Rand { return rand.New(rand.NewPCG(s.rng.Uint64(), s.rng.Uint64())) }
	until := timeutil.Now().Add(s.runFor)
	var wg sync.WaitGroup
	for w := 0; w < s.workers; w++ {
		wrng := newRNG()
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			s.worker(ctx, w, wrng, until)
		}(w)
	}
	frng := newRNG()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.faults(frng, until)
	}()
	wg.Wait()
	s.endPhase("workload")

	if err := s.heal(ctx); err != nil {
		return errors.Wrap(err, "after the faults")
	}
	if err := s.check(ctx); err != nil {
		return errors.Wrap(err, "check")
	}
	s.endPhase("check")
	if err := s.checkPITR(ctx); err != nil {
		return errors.Wrap(err, "PITR check")
	}
	s.endPhase("pitr")
	return nil
}

func (s *scenario) endPhase(name string) {
	if s.phase != nil {
		s.phase(name)
	}
}

func (s *scenario) fail(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, fmt.Sprintf(format, args...))
}

func (s *scenario) count(what string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats[what]++
}

// exec runs a statement and fails the seed on an internal error. It
// returns the error, if any, for the caller to judge.
func (s *scenario) exec(ctx context.Context, conn *pgx.Conn, sql string, args ...any) error {
	_, err := conn.Exec(ctx, sql, args...)
	if err != nil && isInternal(err) {
		s.fail("internal error: %s: %v", sql, err)
	}
	return err
}

// setup creates the schema, IMPORTs the data from the fake S3 and creates
// the bank and ledger tables. No faults run yet.
func (s *scenario) setup(ctx context.Context) error {
	conn, err := connect(ctx, s.env.addrs()[0])
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	store, err := backup.OpenStore(ctx, backup.Repository{Bucket: s3Bucket, Endpoint: s.env.s3Endpoint(), Region: s3Region})
	if err != nil {
		return err
	}
	for _, t := range s.tables {
		if err := s.exec(ctx, conn, t.create()); err != nil {
			return errors.Wrapf(err, "%s", t.create())
		}
		name := t.name + ".csv"
		if err := store.Put(ctx, "import/"+name, bytes.NewReader(t.csv(s.rng))); err != nil {
			return err
		}
		uri := fmt.Sprintf("s3://%s/import/%s?AWS_ENDPOINT=%s&AWS_ACCESS_KEY_ID=%s&AWS_SECRET_ACCESS_KEY=%s&AWS_REGION=%s",
			s3Bucket, name, s.env.s3Endpoint(), s3Key, s3Secret, s3Region)
		if err := s.exec(ctx, conn, fmt.Sprintf("IMPORT INTO %s CSV DATA ('%s')", t.name, uri)); err != nil {
			return errors.Wrapf(err, "IMPORT INTO %s", t.name)
		}
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+t.name).Scan(&n); err != nil {
			return err
		}
		if n != t.rows {
			s.fail("IMPORT INTO %s loaded %d rows of %d", t.name, n, t.rows)
		}
	}
	stmts := []string{
		"CREATE TABLE accounts (id INT8 PRIMARY KEY, balance INT8 NOT NULL)",
		fmt.Sprintf("INSERT INTO accounts SELECT i, %d FROM generate_series(0, %d) AS g(i)", bankTotal/s.accounts, s.accounts-1),
		"CREATE TABLE ledger (k INT8 PRIMARY KEY, v INT8 NOT NULL)",
	}
	for _, st := range stmts {
		if err := s.exec(ctx, conn, st); err != nil {
			return errors.Wrapf(err, "%s", st)
		}
	}
	return nil
}

// worker runs random statements against random nodes until the end of the
// run. It reconnects when its node fails it.
func (s *scenario) worker(ctx context.Context, w int, rng *rand.Rand, until time.Time) {
	var conn *pgx.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close(ctx)
		}
	}()
	next := int64(w) << 32
	for timeutil.Now().Before(until) {
		if conn == nil {
			addrs := s.env.addrs()
			c, err := connect(ctx, addrs[rng.IntN(len(addrs))])
			if err != nil {
				time.Sleep(time.Duration(100+rng.IntN(900)) * time.Millisecond)
				continue
			}
			conn = c
		}
		op := rng.IntN(10)
		err := timeutil.RunWithTimeout(ctx, "statement", 15*time.Second, func(ctx context.Context) error {
			switch {
			case op < 3:
				t := s.tables[rng.IntN(len(s.tables))]
				s.count("write")
				return s.exec(ctx, conn, t.write(rng))
			case op < 5:
				q := genQuery(rng, s.tables)
				s.keepQuery(q)
				s.count("query")
				return s.exec(ctx, conn, q.render(noHint))
			case op < 8:
				s.count("transfer")
				return s.transfer(ctx, conn, rng)
			default:
				next++
				s.count("ledger")
				return s.writeLedger(ctx, conn, w, rng, next)
			}
		})
		if err != nil && conn.IsClosed() {
			conn = nil
		}
		time.Sleep(time.Duration(rng.Int64N(int64(2 * s.think))))
	}
}

// keepQuery keeps a comparable query, up to 40, for the check to compare
// its plans.
func (s *scenario) keepQuery(q query) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.comparable && len(s.queries) < 40 {
		s.queries = append(s.queries, q)
	}
}

func noHint(table) string { return "" }

// transfer moves money between two accounts, writing balances computed
// from what it read: the total stays the same only if the transaction is
// serializable.
func (s *scenario) transfer(ctx context.Context, conn *pgx.Conn, rng *rand.Rand) error {
	a, b := rng.IntN(s.accounts), rng.IntN(s.accounts)
	if a == b {
		return nil
	}
	amount := int64(1 + rng.IntN(20))
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ba, bb int64
	if err = tx.QueryRow(ctx, "SELECT balance FROM accounts WHERE id = $1", a).Scan(&ba); err != nil {
		return s.judge(err)
	}
	if err = tx.QueryRow(ctx, "SELECT balance FROM accounts WHERE id = $1", b).Scan(&bb); err != nil {
		return s.judge(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE accounts SET balance = $1 WHERE id = $2", ba-amount, a); err != nil {
		return s.judge(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE accounts SET balance = $1 WHERE id = $2", bb+amount, b); err != nil {
		return s.judge(err)
	}
	return s.judge(tx.Commit(ctx))
}

func (s *scenario) judge(err error) error {
	if err != nil && isInternal(err) {
		s.fail("internal error: %v", err)
	}
	return err
}

// writeLedger upserts one of the worker's own keys and records the
// outcome: acknowledged, or unknown after any error.
func (s *scenario) writeLedger(
	ctx context.Context, conn *pgx.Conn, w int, rng *rand.Rand, v int64,
) error {
	k := w + s.workers*rng.IntN(s.keys/s.workers)
	err := s.exec(ctx, conn, "UPSERT INTO ledger VALUES ($1, $2)", k, v)
	needPITR := s.recordLedger(k, v, err == nil)
	if err == nil && needPITR && rng.IntN(20) == 0 {
		// A time after this acknowledged write, for the PITR check.
		var ts string
		if conn.QueryRow(ctx, "SELECT cluster_logical_timestamp()::STRING").Scan(&ts) == nil {
			if t, perr := hlc.ParseHLC(ts); perr == nil {
				s.setPITR(t)
			}
		}
	}
	return err
}

// recordLedger records a write of v to the ledger key k, acknowledged or of
// unknown outcome. It reports whether the PITR check still needs a time.
func (s *scenario) recordLedger(k int, v int64, acked bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lk := s.ledger[k]
	if lk == nil {
		lk = &ledgerKey{}
		s.ledger[k] = lk
	}
	if acked {
		lk.acked, lk.hasAcked, lk.ambiguous = v, true, nil
	} else {
		lk.ambiguous = append(lk.ambiguous, v)
	}
	return s.pitr.IsEmpty()
}

func (s *scenario) setPITR(t hlc.Timestamp) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pitr.IsEmpty() {
		s.pitr = t
	}
}

// faults injects faults until the end of the run: crashes and restarts,
// partitions, network delay, a power loss of the whole cluster, crashes
// in fsync, and S3 errors and stalls.
func (s *scenario) faults(rng *rand.Rand, until time.Time) {
	n := len(s.env.addrs())
	powerLoss := rng.IntN(3) == 0
	for {
		time.Sleep(time.Duration(500+rng.IntN(4000)) * time.Millisecond)
		if timeutil.Now().After(until) {
			return
		}
		i := rng.IntN(n)
		hold := time.Duration(1000+rng.IntN(6000)) * time.Millisecond
		switch rng.IntN(6) {
		case 0:
			if !s.env.crash(i) {
				continue
			}
			s.count("fault: crash")
			time.Sleep(hold)
			s.env.restart(i, rng.IntN(2) == 0)
		case 1:
			s.count("fault: partition")
			s.env.partition(i, true)
			time.Sleep(hold)
			s.env.partition(i, false)
		case 2:
			j := rng.IntN(n)
			if j == i {
				continue
			}
			s.count("fault: delay")
			s.env.delay(i, j, time.Duration(10+rng.IntN(400))*time.Millisecond)
			time.Sleep(hold)
			s.env.delay(i, j, 0)
		case 3:
			s.count("fault: s3")
			s.env.s3Faults(true)
			time.Sleep(hold)
			s.env.s3Faults(false)
		case 4:
			if !s.env.syncFaults(i, true) {
				continue
			}
			s.count("fault: crash in fsync")
			time.Sleep(hold)
			s.env.syncFaults(i, false)
		case 5:
			if !powerLoss {
				continue
			}
			powerLoss = false
			s.count("fault: power loss")
			var down []int
			for o := 0; o < n; o++ {
				if s.env.crash(o) {
					down = append(down, o)
				}
			}
			time.Sleep(hold)
			for _, o := range down {
				s.env.restart(o, rng.IntN(2) == 0)
			}
		}
	}
}

// heal ends every fault and waits until every node serves SQL again.
func (s *scenario) heal(ctx context.Context) error {
	s.env.s3Faults(false)
	n := len(s.env.addrs())
	for i := 0; i < n; i++ {
		s.env.syncFaults(i, false)
		s.env.partition(i, false)
		for j := 0; j < n; j++ {
			if j != i {
				s.env.delay(i, j, 0)
			}
		}
	}
	for _, addr := range s.env.addrs() {
		if err := waitSQL(ctx, addr, 3*time.Minute); err != nil {
			return err
		}
	}
	return nil
}

// query runs sql in a transaction at ts with the session settings and
// returns its rows as text.
func queryAt(
	ctx context.Context, conn *pgx.Conn, ts string, settings []string, sql string,
) (result, error) {
	for _, set := range settings {
		if _, err := conn.Exec(ctx, set); err != nil {
			return nil, errors.Wrapf(err, "%s", set)
		}
	}
	defer func() {
		_, _ = conn.Exec(ctx, "ROLLBACK")
		_, _ = conn.Exec(ctx, "RESET ALL")
	}()
	if _, err := conn.Exec(ctx, fmt.Sprintf("BEGIN AS OF SYSTEM TIME %s", ts)); err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, sql, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res result
	for rows.Next() {
		raw := rows.RawValues()
		vals := make([]string, len(raw))
		for i, v := range raw {
			if v == nil {
				vals[i] = "NULL"
			} else {
				vals[i] = normalize(string(v))
			}
		}
		res = append(res, strings.Join(vals, "\t"))
	}
	return res, rows.Err()
}

// The settings that change plans without changing results.
var planSettings = [][]string{
	{"SET vectorize = off"},
	{"SET distsql = off"},
	{"SET distsql = always"},
	{"SET reorder_joins_limit = 0"},
	{"SET optimizer_use_histograms = false", "SET optimizer_use_multicol_stats = false"},
	{"SET enable_zigzag_join = true"},
}

// compareQueries runs every recorded query at one timestamp with the
// default plan and with other plans and settings, and fails the seed when
// the results differ.
func (s *scenario) compareQueries(ctx context.Context, conn *pgx.Conn) error {
	var ts string
	if err := conn.QueryRow(ctx, "SELECT cluster_logical_timestamp()::STRING").Scan(&ts); err != nil {
		return err
	}
	for _, q := range s.queries {
		base, err := queryAt(ctx, conn, ts, nil, q.render(noHint))
		if err != nil {
			_ = s.judge(err)
			continue
		}
		for i := 0; i < 2; i++ {
			var settings []string
			hint := noHint
			if s.rng.IntN(3) == 0 {
				// Force an index of each table.
				hint = func(t table) string {
					if len(t.indexes) == 0 || s.rng.IntN(2) == 0 {
						return "@{FORCE_INDEX=primary}"
					}
					return fmt.Sprintf("@{FORCE_INDEX=%s_i%d}", t.name, s.rng.IntN(len(t.indexes)))
				}
			} else {
				settings = planSettings[s.rng.IntN(len(planSettings))]
			}
			sql := q.render(hint)
			other, err := queryAt(ctx, conn, ts, settings, sql)
			if err != nil {
				_ = s.judge(err)
				continue
			}
			s.count("query: compared")
			if !sameResult(base, other, q.ordered) {
				s.fail("results differ at %s:\n%s\nwith the default plan, against\n%s\nwith %v:\n%s",
					ts, q.render(noHint), sql, settings, diffResult(base, other))
			}
		}
	}
	return nil
}

// check runs the checks that need the healed cluster.
func (s *scenario) check(ctx context.Context) error {
	addrs := s.env.addrs()
	conn, err := connect(ctx, addrs[s.rng.IntN(len(addrs))])
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	// Every acknowledged write survived.
	rows, err := conn.Query(ctx, "SELECT k, v FROM ledger")
	if err != nil {
		return err
	}
	found := make(map[int]int64)
	for rows.Next() {
		var k int
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		found[k] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for k, lk := range s.ledger {
		v, ok := found[k]
		if !lk.allows(v, ok) {
			s.fail("ledger key %d holds %d (present %v), but the last acknowledged write was %d (acknowledged %v), with %v unknown after it",
				k, v, ok, lk.acked, lk.hasAcked, lk.ambiguous)
		}
	}
	for k, v := range found {
		if s.ledger[k] == nil {
			s.fail("ledger key %d holds %d, which no write wrote", k, v)
		}
	}

	// The transfers kept the total.
	var total int64
	if err := conn.QueryRow(ctx, "SELECT sum(balance) FROM accounts").Scan(&total); err != nil {
		return err
	}
	if want := int64(bankTotal / s.accounts * s.accounts); total != want {
		s.fail("the accounts hold %d in total, not %d: a transfer was not serializable", total, want)
	}

	return s.compareQueries(ctx, conn)
}

// checkPITR restores the cluster to the time the workload recorded, into a
// new cluster, and compares every table with the source at that time.
func (s *scenario) checkPITR(ctx context.Context) error {
	if s.pitr.IsEmpty() {
		s.count("pitr: no time recorded")
		return nil
	}
	store, err := backup.OpenStore(ctx, backup.Repository{
		Bucket: s3Bucket, Prefix: "cluster", Endpoint: s.env.s3Endpoint(), Region: s3Region,
	})
	if err != nil {
		return err
	}
	deadline := timeutil.Now().Add(5 * time.Minute)
	for {
		_, cerr := backup.CheckTarget(ctx, store, s.pitr)
		if cerr == nil {
			break
		}
		if strings.Contains(cerr.Error(), "no full backup ends at or before") {
			// The first full backup ended after the time: there is nothing
			// to restore to it from.
			s.count("pitr: time before the first full backup")
			return nil
		}
		if timeutil.Now().After(deadline) {
			s.fail("the archive does not reach %s five minutes after the workload: %v", s.pitr, cerr)
			return nil
		}
		time.Sleep(time.Second)
	}

	tables := []string{"accounts", "ledger"}
	for _, t := range s.tables {
		tables = append(tables, t.name)
	}
	src, err := connect(ctx, s.env.addrs()[0])
	if err != nil {
		return err
	}
	defer func() { _ = src.Close(ctx) }()
	want := make(map[string]result)
	for _, t := range tables {
		r, qerr := queryAt(ctx, src, s.pitr.AsOfSystemTime(), nil, "SELECT * FROM "+t+" ORDER BY 1")
		if qerr != nil {
			return errors.Wrapf(qerr, "reading %s at %s", t, s.pitr)
		}
		want[t] = r
	}

	addr, err := s.env.startRestoreNode(ctx)
	if err != nil {
		return err
	}
	// The restore runs from the system database, and may take longer than
	// a statement's usual timeout.
	sys, err := connectDB(ctx, addr, "system")
	if err != nil {
		return err
	}
	defer func() { _ = sys.Close(ctx) }()
	if _, err = sys.Exec(ctx, "SET statement_timeout = '5m'"); err != nil {
		return err
	}
	if _, err = sys.Exec(ctx, "SELECT crdb_internal.restore_cluster($1, $2, $3, $4, $5)",
		s3Bucket, "cluster", s.env.s3Endpoint(), s3Region, s.pitr.AsOfSystemTime()); err != nil {
		s.fail("restoring to %s: %v", s.pitr, err)
		return nil
	}
	dst, err := connect(ctx, addr)
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close(ctx) }()
	for _, t := range tables {
		got, qerr := queryAt(ctx, dst, "'-1us'", nil, "SELECT * FROM "+t+" ORDER BY 1")
		if qerr != nil {
			s.fail("reading %s after the restore to %s: %v", t, s.pitr, qerr)
			continue
		}
		if !sameResult(want[t], got, true) {
			s.fail("table %s restored to %s differs from the source at that time:\n%s", t, s.pitr, diffResult(want[t], got))
		}
	}
	s.count("pitr: checked")
	return nil
}

// report is the seed's summary for the log.
func (s *scenario) report() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b bytes.Buffer
	fmt.Fprintf(&b, "%d tables, %d workers, %d accounts, %d ledger keys, run %s, think %s\n",
		len(s.tables), s.workers, s.accounts, s.keys, s.runFor, s.think)
	for _, t := range s.tables {
		fmt.Fprintf(&b, "  %s; %d rows\n", t.create(), t.rows)
	}
	keys := make([]string, 0, len(s.stats))
	for k := range s.stats {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s: %d\n", k, s.stats[k])
	}
	return b.String()
}
