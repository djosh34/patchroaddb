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
	"fmt"
	"math/rand/v2"
	"strings"
)

// This file generates what a seed runs: the schema, the data that IMPORT
// loads, and the SQL statements. Everything comes from one *rand.Rand, so
// the seed decides it.

// colType is the SQL type of a generated column. There are no floats: the
// results are compared exactly, and a float sum depends on the order of
// its terms.
type colType string

const (
	typInt     colType = "INT8"
	typString  colType = "STRING"
	typBool    colType = "BOOL"
	typDecimal colType = "DECIMAL"
)

var colTypes = []colType{typInt, typString, typBool, typDecimal}

type column struct {
	name string
	typ  colType
}

// table is a generated table. Its first column is id INT8, the primary key.
type table struct {
	name    string
	cols    []column
	indexes [][]string
	rows    int
}

// genSchema returns one to three tables, each with a few columns and
// secondary indexes, and the number of rows IMPORT loads into each.
func genSchema(rng *rand.Rand) []table {
	tables := make([]table, 1+rng.IntN(3))
	for i := range tables {
		t := table{name: fmt.Sprintf("t%d", i), cols: []column{{"id", typInt}}, rows: 20 + rng.IntN(200)}
		for j := 0; j < 2+rng.IntN(4); j++ {
			t.cols = append(t.cols, column{fmt.Sprintf("c%d", j), colTypes[rng.IntN(len(colTypes))]})
		}
		for j := 0; j < rng.IntN(3); j++ {
			idx := []string{t.cols[1+rng.IntN(len(t.cols)-1)].name}
			if rng.IntN(2) == 0 {
				other := t.cols[1+rng.IntN(len(t.cols)-1)].name
				if other != idx[0] {
					idx = append(idx, other)
				}
			}
			t.indexes = append(t.indexes, idx)
		}
		tables[i] = t
	}
	return tables
}

func (t table) create() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (id INT8 PRIMARY KEY", t.name)
	for _, c := range t.cols[1:] {
		fmt.Fprintf(&b, ", %s %s", c.name, c.typ)
	}
	for i, idx := range t.indexes {
		fmt.Fprintf(&b, ", INDEX %s_i%d (%s)", t.name, i, strings.Join(idx, ", "))
	}
	b.WriteString(")")
	return b.String()
}

// csv returns the rows that IMPORT loads: ids 0 to t.rows-1, with an
// empty field for NULL.
func (t table) csv(rng *rand.Rand) []byte {
	var b bytes.Buffer
	for id := 0; id < t.rows; id++ {
		fmt.Fprintf(&b, "%d", id)
		for _, c := range t.cols[1:] {
			b.WriteByte(',')
			if rng.IntN(8) != 0 {
				b.WriteString(csvValue(rng, c.typ))
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// The values are drawn from small domains, so that predicates, joins and
// groups match more than one row.
func csvValue(rng *rand.Rand, typ colType) string {
	switch typ {
	case typInt:
		return fmt.Sprint(rng.IntN(40) - 10)
	case typString:
		return string(rune('a'+rng.IntN(6))) + string(rune('a'+rng.IntN(3)))
	case typBool:
		return fmt.Sprint(rng.IntN(2) == 0)
	default:
		return fmt.Sprintf("%d.%02d", rng.IntN(20)-5, rng.IntN(100))
	}
}

// literal returns a SQL literal of the type, NULL now and then.
func literal(rng *rand.Rand, typ colType) string {
	if rng.IntN(12) == 0 {
		return "NULL"
	}
	v := csvValue(rng, typ)
	switch typ {
	case typString:
		return "'" + v + "'"
	case typDecimal:
		return v + "::DECIMAL"
	default:
		return v
	}
}

func (t table) col(rng *rand.Rand) column { return t.cols[rng.IntN(len(t.cols))] }

// predicate returns a boolean expression over the table's columns, each
// qualified with alias.
func predicate(rng *rand.Rand, t table, alias string, depth int) string {
	if depth > 0 && rng.IntN(3) == 0 {
		l, r := predicate(rng, t, alias, depth-1), predicate(rng, t, alias, depth-1)
		switch rng.IntN(3) {
		case 0:
			return "(" + l + " AND " + r + ")"
		case 1:
			return "(" + l + " OR " + r + ")"
		default:
			return "NOT (" + l + ")"
		}
	}
	c := t.col(rng)
	ref := alias + "." + c.name
	switch rng.IntN(6) {
	case 0:
		if rng.IntN(2) == 0 {
			return ref + " IS NULL"
		}
		return ref + " IS NOT NULL"
	case 1:
		vals := make([]string, 1+rng.IntN(4))
		for i := range vals {
			vals[i] = literal(rng, c.typ)
		}
		return ref + " IN (" + strings.Join(vals, ", ") + ")"
	case 2:
		if c.typ == typBool {
			return ref
		}
		lo, hi := literal(rng, c.typ), literal(rng, c.typ)
		return ref + " BETWEEN " + lo + " AND " + hi
	default:
		ops := []string{"=", "!=", "<", "<=", ">", ">="}
		return ref + " " + ops[rng.IntN(len(ops))] + " " + literal(rng, c.typ)
	}
}

// write returns a statement that changes the table. Ids go up to twice
// the imported rows, so inserts both add rows and conflict.
func (t table) write(rng *rand.Rand) string {
	id := rng.IntN(2 * t.rows)
	switch rng.IntN(5) {
	case 0, 1:
		verb := "UPSERT INTO"
		suffix := ""
		if rng.IntN(2) == 0 {
			verb, suffix = "INSERT INTO", " ON CONFLICT (id) DO NOTHING"
		}
		names := make([]string, len(t.cols))
		vals := make([]string, len(t.cols))
		for i, c := range t.cols {
			names[i] = c.name
			vals[i] = literal(rng, c.typ)
		}
		vals[0] = fmt.Sprint(id)
		return fmt.Sprintf("%s %s (%s) VALUES (%s)%s", verb, t.name,
			strings.Join(names, ", "), strings.Join(vals, ", "), suffix)
	case 2, 3:
		c := t.cols[1+rng.IntN(len(t.cols)-1)]
		return fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s", t.name, c.name, literal(rng, c.typ), predicate(rng, t, t.name, 1))
	default:
		return fmt.Sprintf("DELETE FROM %s WHERE %s", t.name, predicate(rng, t, t.name, 1))
	}
}

// query is a generated SELECT. A query whose answer may differ between two
// correct runs, from a LIMIT without an ORDER BY of every column or from a
// volatile function, runs but is not compared. An ordered query's rows
// are compared in order, any other's as a multiset.
type query struct {
	// sql has %s where a table's name goes, with an index hint for some
	// plans; tables lists those tables in order.
	sql        string
	tables     []table
	ordered    bool
	comparable bool
}

// render returns the query with each table's name followed by hint(t): a
// FORCE_INDEX hint, or nothing.
func (q query) render(hint func(t table) string) string {
	args := make([]any, len(q.tables))
	for i, t := range q.tables {
		args[i] = t.name + hint(t)
	}
	return fmt.Sprintf(q.sql, args...)
}

func genQuery(rng *rand.Rand, tables []table) query {
	t := tables[rng.IntN(len(tables))]
	q := query{tables: []table{t}, comparable: true}
	var cols []string
	switch rng.IntN(4) {
	case 0:
		// An aggregation, grouped by one column.
		g := t.cols[1+rng.IntN(len(t.cols)-1)]
		a := t.col(rng)
		agg := []string{"count(*)", fmt.Sprintf("count(x.%s)", a.name), fmt.Sprintf("min(x.%s)", a.name), fmt.Sprintf("max(x.%s)", a.name)}
		if a.typ == typInt || a.typ == typDecimal {
			agg = append(agg, fmt.Sprintf("sum(x.%s)", a.name))
		}
		cols = []string{"x." + g.name, agg[rng.IntN(len(agg))]}
		q.sql = fmt.Sprintf("SELECT %s FROM %%s AS x WHERE %s GROUP BY x.%s", strings.Join(cols, ", "), predicate(rng, t, "x", 2), g.name)
	case 1:
		if len(tables) > 1 {
			// A join on two columns of the same type.
			u := tables[rng.IntN(len(tables))]
			for _, c := range t.cols {
				for _, d := range u.cols {
					if c.typ == d.typ && q.sql == "" && rng.IntN(3) == 0 {
						cols = []string{"x.id", "y.id", "x." + c.name}
						q.tables = append(q.tables, u)
						q.sql = fmt.Sprintf("SELECT %s FROM %%s AS x JOIN %%s AS y ON x.%s = y.%s WHERE %s AND %s",
							strings.Join(cols, ", "), c.name, d.name, predicate(rng, t, "x", 1), predicate(rng, u, "y", 1))
					}
				}
			}
		}
		if q.sql != "" {
			break
		}
		fallthrough
	default:
		for _, c := range t.cols {
			if rng.IntN(2) == 0 || len(cols) == 0 {
				cols = append(cols, "x."+c.name)
			}
		}
		distinct := ""
		if rng.IntN(4) == 0 {
			distinct = "DISTINCT "
		}
		q.sql = fmt.Sprintf("SELECT %s%s FROM %%s AS x WHERE %s", distinct, strings.Join(cols, ", "), predicate(rng, t, "x", 2))
	}
	if rng.IntN(3) == 0 {
		// ORDER BY every output column fixes the order.
		order := make([]string, len(cols))
		for i := range cols {
			order[i] = fmt.Sprint(i + 1)
		}
		q.sql += " ORDER BY " + strings.Join(order, ", ")
		q.ordered = true
	}
	if rng.IntN(4) == 0 {
		q.sql += fmt.Sprintf(" LIMIT %d", 1+rng.IntN(10))
		q.comparable = q.ordered
	}
	if rng.IntN(30) == 0 {
		// A volatile function: the query runs, but its answer is not fixed.
		q.sql = strings.Replace(q.sql, " FROM ", ", random() < 0.5 FROM ", 1)
		q.comparable = false
	}
	return q
}
