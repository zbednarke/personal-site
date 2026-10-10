package main

import (
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Idea space data: every Moment as a bubble, Ideas (margin notes of type
// "idea"), Threads (currents), People (lenses) and the owner's region names.
// The browser computes a deterministic layout from this; nothing is named by
// the system — regions and threads stay nameless until the owner names them.

type cpSpaceMoment struct {
	ID         uuid.UUID   `json:"id"`
	Kind       string      `json:"kind"`
	Title      string      `json:"title"`
	Excerpt    string      `json:"excerpt"`
	OccurredAt *time.Time  `json:"occurredAt"`
	CreatedAt  time.Time   `json:"createdAt"`
	Timezone   string      `json:"timezone"`
	Source     string      `json:"source"`
	Detail     string      `json:"sourceDetail"`
	People     []uuid.UUID `json:"people"`
	Senders    []uuid.UUID `json:"senders"`
	Images     int         `json:"images"`
	Lines      int         `json:"lines"`
}

type cpSpaceIdea struct {
	ID       uuid.UUID  `json:"id"`
	MomentID uuid.UUID  `json:"momentId"`
	LineID   *uuid.UUID `json:"lineId,omitempty"`
	Title    string     `json:"title"`
	Body     string     `json:"body"`
	State    string     `json:"state"`
}

type cpSpaceThread struct {
	ID       uuid.UUID `json:"id"`
	Title    string    `json:"title"`
	Revision int64     `json:"revision"`
	Knots    []cpKnot  `json:"knots"`
}

type cpRegionName struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (app *application) cpSpace(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rows, err := app.db.Query(ctx, `SELECT m.id,m.kind,m.title,`+cpExcerptSQL+`,m.occurred_at,m.created_at,m.timezone,m.source,m.source_detail,
 coalesce((SELECT array_agg(DISTINCT mp.person_id) FROM cp_moment_people mp WHERE mp.moment_id=m.id),'{}'),
 coalesce((SELECT array_agg(mp.person_id ORDER BY mp.position) FROM cp_moment_people mp WHERE mp.moment_id=m.id AND mp.role='sender'),'{}'),
 (SELECT count(*) FROM cp_artifacts a WHERE a.moment_id=m.id AND a.kind='image')::int,
 (SELECT count(*) FROM cp_lines l WHERE l.moment_id=m.id)::int
 FROM cp_moments m WHERE m.user_id=$1 ORDER BY coalesce(m.occurred_at,m.created_at),m.id LIMIT 20000`, user)
	moments, err := cpCollect(rows, err, func(row pgx.Row) (cpSpaceMoment, error) {
		var m cpSpaceMoment
		err := row.Scan(&m.ID, &m.Kind, &m.Title, &m.Excerpt, &m.OccurredAt, &m.CreatedAt, &m.Timezone, &m.Source, &m.Detail, &m.People, &m.Senders, &m.Images, &m.Lines)
		return m, err
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	rows, err = app.db.Query(ctx, `SELECT n.id,n.moment_id,n.line_id,n.title,n.body,n.state FROM cp_annotations n
 WHERE n.user_id=$1 AND n.note_type='idea' AND n.state<>'erased' ORDER BY n.moment_id,n.created_at LIMIT 20000`, user)
	ideas, err := cpCollect(rows, err, func(row pgx.Row) (cpSpaceIdea, error) {
		var i cpSpaceIdea
		err := row.Scan(&i.ID, &i.MomentID, &i.LineID, &i.Title, &i.Body, &i.State)
		return i, err
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	rows, err = app.db.Query(ctx, `SELECT t.id,t.title,t.revision FROM cp_threads t WHERE t.user_id=$1 AND EXISTS(SELECT 1 FROM cp_thread_knots k WHERE k.thread_id=t.id) ORDER BY t.created_at`, user)
	threads, err := cpCollect(rows, err, func(row pgx.Row) (cpSpaceThread, error) {
		var t cpSpaceThread
		err := row.Scan(&t.ID, &t.Title, &t.Revision)
		return t, err
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	for i := range threads {
		if threads[i].Knots, err = cpLoadKnots(ctx, app.db, threads[i].ID); err != nil {
			app.serverError(w, err)
			return
		}
	}
	rows, err = app.db.Query(ctx, `SELECT p.id,p.display_name,p.aliases,p.special,(SELECT count(DISTINCT mp.moment_id) FROM cp_moment_people mp WHERE mp.person_id=p.id)::int
 FROM cp_people p WHERE p.user_id=$1 ORDER BY 5 DESC,p.display_name`, user)
	people, err := cpCollect(rows, err, func(row pgx.Row) (cpPerson, error) {
		var p cpPerson
		err := row.Scan(&p.ID, &p.Name, &p.Aliases, &p.Special, &p.Moments)
		if p.Aliases == nil {
			p.Aliases = []string{}
		}
		return p, err
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	rows, err = app.db.Query(ctx, `SELECT region_key,name FROM cp_regions WHERE user_id=$1 ORDER BY region_key`, user)
	regions, err := cpCollect(rows, err, func(row pgx.Row) (cpRegionName, error) {
		var g cpRegionName
		return g, row.Scan(&g.Key, &g.Name)
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"moments": moments, "ideas": ideas, "threads": threads, "people": people, "regions": regions})
}

var cpRegionKey = regexp.MustCompile(`^[a-z]+:[A-Za-z0-9:_-]{1,120}$`)

// cpNameRegion stores (or, with an empty name, clears) the owner's name for a
// region of the Idea space, keyed by the client's stable cluster key.
func (app *application) cpNameRegion(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !cpRegionKey.MatchString(key) {
		writeError(w, 400, "invalid region key")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid region JSON")
		return
	}
	name, err := cpOneLine(in.Name, 120, "name")
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	if name == "" {
		_, err = app.db.Exec(r.Context(), `DELETE FROM cp_regions WHERE user_id=$1 AND region_key=$2`, user, key)
	} else {
		_, err = app.db.Exec(r.Context(), `INSERT INTO cp_regions(user_id,region_key,name) VALUES($1,$2,$3)
 ON CONFLICT (user_id,region_key) DO UPDATE SET name=EXCLUDED.name,updated_at=now()`, user, key, name)
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"region": cpRegionName{Key: key, Name: name}})
}
