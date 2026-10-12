package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// previewFixList seeds a fictional day of failures for the Workbench
// preview's owner and sends that day's fix list (the preview has no push
// service). tools/workbench/browser-check.cjs calls it last.
func previewFixList(ctx context.Context, app *application) error {
	user, err := app.userID(context.WithValue(ctx, userSubjectKey, "visual-test"))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	horn, run := uuid.New(), uuid.New()
	type stmt struct {
		sql  string
		args []any
	}
	stmts := []stmt{
		{`INSERT INTO trumpet_horns(id,user_id,maker,model) VALUES($1,$2,'Fixture','Model A')`, []any{horn, user}},
		{`INSERT INTO trumpet_runs(id,user_id,external_id,kind,status,error,started_at,completed_at) VALUES($1,$2,'adaptive-watch-preview','combined','partial',
			'Reverb API search failed; Recheck failed, recorded for retry: shop.example / timed out; Discovery page budget (240) reached',$3,$3::timestamptz+interval '31 minutes')`, []any{run, user, now.Add(-40 * time.Minute)}},
		{`INSERT INTO trumpet_run_sources(run_id,source,status,note,domain) VALUES($1,'Reverb API','failed','Reverb API failed: HTTP 429','reverb.com'),
			($1,'Maker search: Fixture','failed','Search provider failed: TimeoutError',''),($1,'Live pages / lead.example','skipped','Unverified search lead discovered; no live page accessed.','lead.example')`, []any{run}},
		{`INSERT INTO api_route_counters(hour,service,route,requests,errors_5xx,slow) VALUES($1,'jazz-api','POST /v1/studio/renders',40,3,2)`, []any{now.Add(-2 * time.Hour).Truncate(time.Hour)}},
		{`INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,status,error,manifest,finished_at,updated_at) VALUES($1,$2,$3,'2026-10-11',60,'failed','render stopped at 12 s','{}',$4,$4)`, []any{uuid.New(), user, uuid.New(), now.Add(-3 * time.Hour)}},
	}
	for i := 0; i < 3; i++ {
		stmts = append(stmts, stmt{`INSERT INTO trumpet_listings(id,user_id,horn_id,source,title,status,discovery_type,verification_state,last_check_attempt,last_check_error,check_failures,canonical_url)
			VALUES($1,$2,$3,'Fixture shop','Fixture listing','active','new listing','verified',$4,'shop.example / timed out',1,$5)`, []any{uuid.New(), user, horn, now.Add(-time.Hour), fmt.Sprintf("https://shop.example/horn-%d", i)}})
	}
	for _, st := range stmts {
		if _, err := app.db.Exec(ctx, st.sql, st.args...); err != nil {
			return err
		}
	}
	_, err = app.fixListDeliver(ctx, user, now, "preview")
	return err
}
