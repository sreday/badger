package snore

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// What the humans app's database looks like to Snore's read-only login:
// table and column names, how many rows, and the words used in the few
// columns that hold a kind or a status. Never a row about a person. Snore
// keeps it in humans-layout.json so the reads can follow the real layout.

type columnLayout struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Values map[string]int `json:"values,omitempty"` // word -> rows, vocabulary columns only
}

type tableLayout struct {
	Table   string         `json:"table"`
	Rows    int64          `json:"rows"`
	Columns []columnLayout `json:"columns"`
}

type humansLayout struct {
	At     time.Time     `json:"at"`
	Tables []tableLayout `json:"tables"`
}

// vocabMax: a column with more distinct words than this is not a vocabulary.
const vocabMax = 30

// vocabColumn: a text column whose name says it holds a kind or a status,
// so its distinct values are a short list of words and never a person.
func vocabColumn(name, typ string) bool {
	if !strings.Contains(typ, "char") && typ != "text" && typ != "USER-DEFINED" {
		return false
	}
	n := strings.ToLower(name)
	for _, w := range []string{"type", "status", "kind", "state", "stage", "channel", "source", "role"} {
		if n == w || strings.HasSuffix(n, "_"+w) {
			return true
		}
	}
	return false
}

// readLayout lists what the login may read, in a read-only transaction.
func readLayout(ctx context.Context, dsn string) ([]tableLayout, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())

	rows, err := tx.Query(ctx, `SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'public' ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, err
	}
	byTable := map[string]*tableLayout{}
	var order []string
	for rows.Next() {
		var t, c, typ string
		if rows.Scan(&t, &c, &typ) != nil {
			continue
		}
		if byTable[t] == nil {
			byTable[t] = &tableLayout{Table: t}
			order = append(order, t)
		}
		byTable[t].Columns = append(byTable[t].Columns, columnLayout{Name: c, Type: typ})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(order)
	var out []tableLayout
	for _, name := range order {
		t := byTable[name]
		id := pgx.Identifier{name}.Sanitize()
		// A table the login may not read as a whole keeps Rows at -1; each
		// try sits in its own savepoint so one refusal does not end the read.
		t.Rows = -1
		if sp, err := tx.Begin(ctx); err == nil {
			if sp.QueryRow(ctx, "SELECT count(*) FROM "+id).Scan(&t.Rows) == nil {
				sp.Commit(ctx)
			} else {
				sp.Rollback(ctx)
			}
		}
		for i := range t.Columns {
			c := &t.Columns[i]
			if !vocabColumn(c.Name, c.Type) {
				continue
			}
			col := pgx.Identifier{c.Name}.Sanitize()
			sp, err := tx.Begin(ctx)
			if err != nil {
				continue
			}
			vals := map[string]int{}
			vr, err := sp.Query(ctx, "SELECT "+col+"::text, count(*) FROM "+id+" WHERE "+col+" IS NOT NULL GROUP BY 1 ORDER BY 2 DESC LIMIT 31")
			if err == nil {
				for vr.Next() {
					var v string
					var n int
					if vr.Scan(&v, &n) == nil {
						vals[v] = n
					}
				}
				err = vr.Err()
			}
			if err != nil {
				sp.Rollback(ctx)
				continue
			}
			sp.Commit(ctx)
			if len(vals) > 0 && len(vals) <= vocabMax {
				c.Values = vals
			}
		}
		out = append(out, *t)
	}
	return out, nil
}

// syncLayout reads the layout once per start and keeps it on disk. Called
// without the lock.
func (s *Server) syncLayout() {
	if s.cfg.HumansDB == "" || s.readLayout == nil {
		return
	}
	tables, err := s.readLayout(context.Background(), s.cfg.HumansDB)
	if err != nil {
		log.Printf("Snore: humans layout: %v", strings.ReplaceAll(err.Error(), s.cfg.HumansDB, "<db>"))
		return
	}
	writeJSON(filepath.Join(s.dir, "humans-layout.json"), humansLayout{At: s.now().UTC(), Tables: tables})
	for _, t := range tables {
		var cols []string
		for _, c := range t.Columns {
			cols = append(cols, c.Name)
		}
		log.Printf("Snore: humans layout: %s (%d rows): %s", t.Table, t.Rows, strings.Join(cols, ", "))
	}
}
