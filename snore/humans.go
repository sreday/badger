package snore

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The humans app is where the team records who they reached out to. Snore
// reads it (a login that can only read) and, when a card's name is exactly
// a name there, shows that person's status and their profile link.

// Humans is what the humans app says about a card's speaker.
type Humans struct {
	Status  string    `json:"status,omitempty"` // the latest status there, or "None"
	Profile string    `json:"profile,omitempty"`
	Speaker bool      `json:"speaker,omitempty"` // a speaker at one of our events
	At      time.Time `json:"at,omitempty"`
}

type humanRow struct {
	slug, status string
	speaker      bool
	at           time.Time
}

// humanKey: exact name, only case and spacing folded.
func humanKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

const (
	qProfiles = `SELECT DISTINCT ON (linked_in) linked_in, name, observed_at FROM profile_metadata
		WHERE deleted_at IS NULL AND coalesce(name,'') <> '' AND coalesce(linked_in,'') <> '' ORDER BY linked_in, observed_at DESC`
	qAttendees = `SELECT linked_in, name, updated_at FROM attendees
		WHERE deleted_at IS NULL AND coalesce(name,'') <> '' AND coalesce(linked_in,'') <> ''`
	qSpeakers = `SELECT DISTINCT linked_in FROM speakers WHERE deleted_at IS NULL AND coalesce(linked_in,'') <> ''`
	qStatus   = `SELECT DISTINCT ON (linked_in) linked_in, event_type FROM user_events
		WHERE deleted_at IS NULL AND coalesce(linked_in,'') <> '' AND event_type <> 'note' ORDER BY linked_in, id DESC`
)

// readHumans loads name -> people from the humans DB, read-only.
func readHumans(ctx context.Context, dsn string) (map[string][]humanRow, error) {
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

	status := map[string]string{}
	rows, err := tx.Query(ctx, qStatus)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var slug, st string
		if rows.Scan(&slug, &st) == nil {
			status[slug] = st
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	speakers := map[string]bool{}
	rows, err = tx.Query(ctx, qSpeakers)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var slug string
		if rows.Scan(&slug) == nil {
			speakers[slug] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]humanRow{}
	bySlug := map[string]map[string]bool{} // name -> slugs already in
	add := func(slug, name, fallback string, at time.Time) {
		k := humanKey(name)
		if k == "" {
			return
		}
		if bySlug[k] == nil {
			bySlug[k] = map[string]bool{}
		}
		if bySlug[k][slug] {
			return
		}
		bySlug[k][slug] = true
		st := status[slug]
		if st == "" && speakers[slug] {
			st = "speaker"
		}
		if st == "" {
			st = fallback
		}
		out[k] = append(out[k], humanRow{slug: slug, status: st, speaker: speakers[slug], at: at})
	}
	for _, q := range []struct{ sql, fallback string }{{qProfiles, "saved"}, {qAttendees, "attendee"}} {
		rows, err := tx.Query(ctx, q.sql)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var slug, name string
			var at *time.Time
			if rows.Scan(&slug, &name, &at) == nil {
				t := time.Time{}
				if at != nil {
					t = *at
				}
				add(slug, name, q.fallback, t)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// humansFor: the humans app's word on one name. One person: their status
// and profile. Several people with that name: the count, no link.
func humansFor(idx map[string][]humanRow, name string, now time.Time) Humans {
	rs := idx[humanKey(name)]
	switch len(rs) {
	case 0:
		return Humans{Status: "None", At: now}
	case 1:
		return Humans{Status: rs[0].status, Profile: "https://www.linkedin.com/in/" + rs[0].slug + "/", Speaker: rs[0].speaker, At: now}
	}
	return Humans{Status: strconv.Itoa(len(rs)) + " people with this name", At: now}
}

// syncHumans reads the humans app and cfp.ninja and stamps every card.
// Called without the lock. With no login set or a DB unreachable, cards
// keep what they had. Someone who became a speaker (a speaker in the humans
// app, or accepted on cfp.ninja) leaves the board.
func (s *Server) syncHumans() {
	var idx map[string][]humanRow
	var cfp map[string]CFP
	var herr, cerr error
	if s.cfg.HumansDB != "" {
		idx, herr = s.readHumans(context.Background(), s.cfg.HumansDB)
	}
	if s.cfg.CFPDB != "" {
		cfp, cerr = s.readCFP(context.Background(), s.cfg.CFPDB)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if herr != nil {
		s.humansErr = "humans app unreachable"
		log.Printf("Snore: humans app: %v", strings.ReplaceAll(herr.Error(), s.cfg.HumansDB, "<db>"))
	} else if idx != nil {
		s.humansErr = ""
		s.humans = idx
	}
	if cerr != nil {
		log.Printf("Snore: cfp.ninja: %v", strings.ReplaceAll(cerr.Error(), s.cfg.CFPDB, "<db>"))
	} else if cfp != nil {
		s.cfp = cfp
	}
	n, m, gone := 0, 0, 0
	for topic, cs := range s.st.Cards {
		var keep []*Card
		for _, c := range cs {
			s.stampHumans(c)
			if c.Humans.Speaker || c.CFP.Status == "accepted" {
				s.st.Graduated[c.Key] = "became a speaker"
				s.note("left", "%s left the board: became a speaker", c.Name)
				gone++
				continue
			}
			if c.Humans.Profile != "" {
				n++
			}
			if c.CFP.URL != "" {
				m++
			}
			keep = append(keep, c)
		}
		s.st.Cards[topic] = keep
	}
	log.Printf("Snore: lookups done, %d humans match(es), %d cfp.ninja match(es), %d became speakers", n, m, gone)
	s.save()
}

// stampHumans applies the last reads to one card (lock held).
func (s *Server) stampHumans(c *Card) {
	if s.humans != nil {
		c.Humans = humansFor(s.humans, c.Name, s.now())
	}
	if s.cfp != nil {
		c.CFP = s.cfp[humanKey(c.Name)]
	}
}
