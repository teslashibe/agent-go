package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewedNoteRecoveryPreservesEvidence(t *testing.T) {
	for _, tc := range []struct{ name, mutate string }{
		{"success", ""},
		{"stale result", `UPDATE tool_operations SET result='changed' WHERE job_id=1`},
		{"stale arguments", `UPDATE tool_operations SET arguments='{"Name":"create_shared_note","Args":{"title":"changed"}}' WHERE job_id=1`},
		{"stale progress", `UPDATE tool_progress SET result='changed' WHERE job_id=1`},
		{"stale note", `UPDATE note_actions SET note_id='changed' WHERE job_id=1`},
		{"stale request", `UPDATE jobs SET prompt='changed' WHERE id=1`},
		{"stale session", `UPDATE sources SET session='changed'`},
		{"wrong source state", `UPDATE sources SET source_invalid=1`},
		{"not paused", `UPDATE sources SET paused=0`},
		{"wrong claim", `UPDATE native_note_claims SET operation_id='another'`},
		{"other running", `UPDATE jobs SET state='running' WHERE id=2`},
		{"other unknown", `UPDATE jobs SET state='unknown' WHERE id=2`},
		{"unresolved reply", `INSERT INTO replies(job_id,ordinal,text,state) VALUES(1,0,'original reply','unknown')`},
		{"unresolved acknowledgement", `UPDATE jobs SET ack_state='unknown' WHERE id=1`},
		{"other tool", `INSERT INTO tool_operations(job_id,operation_id,arguments,state) VALUES(2,'other','{}','unknown')`},
		{"unresolved reminder", `INSERT INTO reminders(source,user_id,text,due_utc,created_zone,status) SELECT source,'user','fixture',1,'UTC','unknown' FROM sources`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			src := Source{Name: "fixture", Sender: "owner", ChatGUID: "chat", ChatID: 1}
			if err = s.Initialize(ctx, src, 0, "original generation"); err != nil {
				t.Fatal(err)
			}
			if err = s.ConfigureNotes(ctx, src, nil); err != nil {
				t.Fatal(err)
			}
			_, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state,error,ack_state) VALUES(1,?,'original-guid','original request',1,'unknown','original failure','submitted');
 INSERT INTO jobs(id,source,guid,prompt,created_at,state) VALUES(2,?,'next-guid','next request',2,'queued');
 UPDATE sources SET paused=1,session='original session';
 INSERT INTO tool_operations(job_id,operation_id,arguments,state,result) VALUES(1,'create','{"Name":"create_shared_note","Args":{"title":"Fixture"}}','unknown','original uncertain result');
 INSERT INTO native_note_claims(job_id,operation_id) VALUES(1,'create');
 INSERT INTO tool_progress(job_id,operation_id,item_index,result) VALUES(1,'create',0,'original progress');
 INSERT INTO note_actions(job_id,source,action,note_id,state,error) VALUES(1,?,'create_shared_note','created-note','unknown','original note error');
 INSERT INTO note_scopes(source,id,title,state) VALUES(?,'created-note','Fixture','sharing_unverified');`, src.key(), src.key(), src.key(), src.key())
			if err != nil {
				t.Fatal(err)
			}
			review, err := s.ReviewNoteAttempt(ctx, src, 1, "create")
			if err != nil {
				t.Fatal(err)
			}
			if tc.mutate != "" {
				if _, err = s.db.Exec(tc.mutate); err != nil {
					t.Fatal(err)
				}
			}
			err = s.ResolveReviewedNoteAttempt(ctx, src, 1, "create", review.SHA256, "Creation exists; sharing remains unverified; no replay; request outstanding", false)
			if (err == nil) != (tc.mutate == "") {
				t.Fatalf("resolution error=%v", err)
			}
			var resolution string
			if err = s.db.QueryRow(`SELECT resolution FROM tool_operations WHERE job_id=1`).Scan(&resolution); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != "" {
				if resolution != "" {
					t.Fatal("rejected review changed resolution")
				}
				var count int
				if err = s.db.QueryRow(`SELECT count(*) FROM tool_operation_reviews`).Scan(&count); err != nil || count != 0 {
					t.Fatal(count, err)
				}
				return
			}
			if resolution != "abandoned" {
				t.Fatal(resolution)
			}
			for query, want := range map[string]string{
				`SELECT state FROM jobs WHERE id=1`:                     "failed",
				`SELECT error FROM jobs WHERE id=1`:                     "original failure",
				`SELECT prompt FROM jobs WHERE id=1`:                    "original request",
				`SELECT state FROM jobs WHERE id=2`:                     "queued",
				`SELECT session FROM sources`:                           "original session",
				`SELECT result FROM tool_operations WHERE job_id=1`:     "original uncertain result",
				`SELECT state FROM tool_operations WHERE job_id=1`:      "unknown",
				`SELECT result FROM tool_progress WHERE job_id=1`:       "original progress",
				`SELECT state FROM note_actions WHERE job_id=1`:         "unknown",
				`SELECT state FROM note_scopes WHERE id='created-note'`: "sharing_unverified",
			} {
				var got string
				if err = s.db.QueryRow(query).Scan(&got); err != nil || got != want {
					t.Fatal(query, got, err)
				}
			}
			if err = s.ResolveReviewedNoteAttempt(ctx, src, 1, "create", review.SHA256, "repeat", false); err == nil {
				t.Fatal("repeated resolution accepted")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			var paused bool
			if err = s.db.QueryRow(`SELECT paused FROM sources`).Scan(&paused); err != nil || paused {
				t.Fatal(paused, err)
			}
			var count int
			if err = s.db.QueryRow(`SELECT count(*) FROM tool_operation_reviews`).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
		})
	}
}

func TestReviewedUnknownAcknowledgementRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, before, after   string
		allow, reject, legacy bool
	}{
		{name: "explicit unknown", allow: true},
		{name: "legacy explicit unknown", allow: true, legacy: true},
		{name: "unknown needs explicit decision", reject: true},
		{name: "dispatching is never abandoned", before: `UPDATE jobs SET ack_state='dispatching' WHERE id=1`, allow: true, reject: true},
		{name: "flag requires unknown", before: `UPDATE jobs SET ack_state='submitted' WHERE id=1`, allow: true, reject: true},
		{name: "stale acknowledgement reaction", after: `UPDATE jobs SET ack_reaction='love' WHERE id=1`, allow: true, reject: true},
		{name: "stale observed outcome", after: `UPDATE jobs SET ack_outcome='accepted' WHERE id=1`, allow: true, reject: true},
		{name: "stale completed sibling", after: `UPDATE tool_operations SET result='changed' WHERE operation_id='completed'`, allow: true, reject: true},
		{name: "stale sibling progress", after: `UPDATE tool_progress SET result='changed' WHERE operation_id='completed'`, allow: true, reject: true},
		{name: "other terminal acknowledgement dispatching", before: `INSERT INTO jobs(id,source,guid,prompt,created_at,state,ack_state) SELECT 2,source,'other','other',1,'completed','dispatching' FROM sources`, allow: true, reject: true},
		{name: "active approval", before: `INSERT INTO approvals(token,source,job_id,sender,thread_id,turn_id,request_id,item_id,kind,fingerprint,description,created_at,expires_at,cursor,delivery,state) SELECT 'fixture',source,1,'owner','thread','turn','request','item','command','hash','fixture',0,1,0,'unknown','waiting' FROM sources`, allow: true, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			src := Source{Name: "fixture", Sender: "owner", ChatGUID: "chat", ChatID: 1}
			if err = s.Initialize(ctx, src, 0, "generation"); err != nil {
				t.Fatal(err)
			}
			if err = s.ConfigureNotes(ctx, src, nil); err != nil {
				t.Fatal(err)
			}
			_, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state,error,ack_state,ack_reaction,ack_outcome) VALUES(1,?,'original-guid','original request',1,'unknown','original failure','unknown','like','unknown');
UPDATE sources SET paused=1,session='original session';
INSERT INTO tool_operations(job_id,operation_id,arguments,state,result) VALUES(1,'create','{"Name":"create_shared_note","Args":{"title":"Fixture"}}','dispatching','original uncertain result');
INSERT INTO native_note_claims(job_id,operation_id) VALUES(1,'create');
INSERT INTO tool_progress(job_id,operation_id,item_index,result) VALUES(1,'create',0,'creation observed');
INSERT INTO tool_operations(job_id,operation_id,arguments,state,result) VALUES(1,'completed','{"Name":"add_note_items"}','completed','original completed result');
INSERT INTO tool_progress(job_id,operation_id,item_index,result) VALUES(1,'completed',0,'original completed progress');
INSERT INTO note_actions(job_id,source,action,note_id,state,error) VALUES(1,?,'create_shared_note','created-note','unknown','original note error');
INSERT INTO note_scopes(source,id,title,state) VALUES(?,'created-note','Fixture','sharing_unverified');`, src.key(), src.key(), src.key())
			if err != nil {
				t.Fatal(err)
			}
			if tc.legacy {
				if _, err = s.db.Exec(`ALTER TABLE jobs DROP COLUMN ack_outcome`); err != nil {
					t.Fatal(err)
				}
			}
			if tc.before != "" {
				if _, err = s.db.Exec(tc.before); err != nil {
					t.Fatal(err)
				}
			}
			// The same opener used by the command leaves historical dispatching
			// effects intact; reviewing must not invoke startup crash recovery.
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenForHistoryImport(path)
			if err != nil {
				t.Fatal(err)
			}
			review, err := s.ReviewNoteAttempt(ctx, src, 1, "create")
			if err != nil {
				t.Fatal(err)
			}
			if tc.after != "" {
				if _, err = s.db.Exec(tc.after); err != nil {
					t.Fatal(err)
				}
			}
			// A rejected resolution must not change any table, including audits.
			before := recoverySnapshot(t, s)
			err = s.ResolveReviewedNoteAttempt(ctx, src, 1, "create", review.SHA256, "Creation observed; sharing and acknowledgement delivery remain unverified; request outstanding", tc.allow)
			if (err != nil) != tc.reject {
				t.Fatalf("recovery error=%v reject=%v", err, tc.reject)
			}
			if tc.reject {
				if after := recoverySnapshot(t, s); after != before {
					t.Fatal("rejected recovery changed durable evidence")
				}
				return
			}
			for query, want := range map[string]string{
				`SELECT state FROM jobs WHERE id=1`:                                  "failed",
				`SELECT error FROM jobs WHERE id=1`:                                  "original failure",
				`SELECT prompt FROM jobs WHERE id=1`:                                 "original request",
				`SELECT guid FROM jobs WHERE id=1`:                                   "original-guid",
				`SELECT ack_state FROM jobs WHERE id=1`:                              "unknown",
				`SELECT ack_reaction FROM jobs WHERE id=1`:                           "like",
				`SELECT session FROM sources`:                                        "original session",
				`SELECT state FROM tool_operations WHERE operation_id='create'`:      "dispatching",
				`SELECT resolution FROM tool_operations WHERE operation_id='create'`: "abandoned",
				`SELECT result FROM tool_operations WHERE operation_id='completed'`:  "original completed result",
				`SELECT result FROM tool_progress WHERE operation_id='completed'`:    "original completed progress",
				`SELECT state FROM note_actions WHERE job_id=1`:                      "unknown",
				`SELECT state FROM note_scopes WHERE id='created-note'`:              "sharing_unverified",
			} {
				var got string
				if err = s.db.QueryRow(query).Scan(&got); err != nil || got != want {
					t.Fatal(query, got, err)
				}
			}
			var hasOutcome bool
			if err = s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('jobs') WHERE name='ack_outcome')`).Scan(&hasOutcome); err != nil || hasOutcome == tc.legacy {
				t.Fatal("review changed outcome schema", err)
			}
			if hasOutcome {
				var outcome string
				if err = s.db.QueryRow(`SELECT ack_outcome FROM jobs WHERE id=1`).Scan(&outcome); err != nil || outcome != "unknown" {
					t.Fatal("outcome changed", outcome, err)
				}
			}
			var detail string
			if err = s.db.QueryRow(`SELECT detail FROM recovery_audit WHERE job_id=1`).Scan(&detail); err != nil || !strings.Contains(detail, "acknowledgement remains unknown") {
				t.Fatal("acknowledgement decision not audited", detail, err)
			}
			before = recoverySnapshot(t, s)
			if err = s.ResolveReviewedNoteAttempt(ctx, src, 1, "create", review.SHA256, "repeat", true); err == nil {
				t.Fatal("repeated resolution accepted")
			}
			if recoverySnapshot(t, s) != before {
				t.Fatal("repeated resolution changed evidence")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			var paused bool
			if err = s.db.QueryRow(`SELECT paused FROM sources`).Scan(&paused); err != nil || paused {
				t.Fatal("restart re-paused abandoned work", err)
			}
			if claimed, _, err := s.ClaimToolOperation(ctx, src, 1, "new-reaction", `{"Name":"react"}`); claimed || err == nil {
				t.Fatal("terminal job replayed")
			}
			if job, reply, err := s.ClaimNext(ctx, src, time.Now()); err != nil || job != nil || reply != nil {
				t.Fatal("restart claimed abandoned work", err)
			}
		})
	}
}

func recoverySnapshot(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var all []any
	for _, name := range tables {
		rows, err := s.db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, name, cols)
		for rows.Next() {
			values := make([]any, len(cols))
			refs := make([]any, len(cols))
			for i := range values {
				refs[i] = &values[i]
			}
			if err = rows.Scan(refs...); err != nil {
				t.Fatal(err)
			}
			all = append(all, values)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	data, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
