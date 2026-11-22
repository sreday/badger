package snore

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// cfp.ninja is where speakers submit talks to our conferences. Snore reads
// it (a login that can read a few columns only): a card whose name is
// exactly a submitting speaker's gets the frog, linking to that event's
// proposals. Someone accepted there is joining a lineup and leaves the board.

const cfpBase = "https://cfp.ninja"

// CFP is a speaker's latest submission on cfp.ninja.
type CFP struct {
	Event  string `json:"event,omitempty"`
	Status string `json:"status,omitempty"`
	URL    string `json:"url,omitempty"`
}

const qProposals = `SELECT p.event_id, e.name, coalesce(p.status, ''), p.speakers FROM proposals p JOIN events e ON e.id = p.event_id
	WHERE p.deleted_at IS NULL AND e.deleted_at IS NULL ORDER BY p.created_at DESC`

func readCFP(ctx context.Context, dsn string) (map[string]CFP, error) {
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
	rows, err := tx.Query(ctx, qProposals)
	if err != nil {
		return nil, err
	}
	out := map[string]CFP{}
	for rows.Next() {
		var id int64
		var event, status string
		var raw []byte
		if rows.Scan(&id, &event, &status, &raw) != nil {
			continue
		}
		var sp []struct {
			Name string `json:"name"`
		}
		json.Unmarshal(raw, &sp)
		for _, s := range sp {
			k := humanKey(s.Name)
			if _, seen := out[k]; k == "" || seen { // newest first: keep the first
				continue
			}
			out[k] = CFP{Event: event, Status: status, URL: cfpBase + "/dashboard/events/" + strconv.FormatInt(id, 10) + "/proposals"}
		}
	}
	return out, rows.Err()
}
