package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveInterruptedAttempt(t *testing.T) {
	for _, tc := range []struct {
		name, mutation string
		reject         bool
	}{
		{"success", "", false},
		{"deadline exceeded", "UPDATE jobs SET error='codex: context deadline exceeded' WHERE id=1", false},
		{"interactive deadline exceeded", "UPDATE jobs SET error='codex: interactive run: context deadline exceeded' WHERE id=1", false},
		{"interactive interrupt", "UPDATE jobs SET error='codex: interactive run: turn failed or interrupted' WHERE id=1", false},
		{"invalid source", "UPDATE sources SET source_invalid=1", true},
		{"not paused", "UPDATE sources SET paused=0", true},
		{"wrong error", "UPDATE jobs SET error='timeout' WHERE id=1", true},
		{"running", "UPDATE jobs SET state='running' WHERE id=1", true},
		{"other running", "UPDATE jobs SET state='running' WHERE id=2", true},
		{"submitted reply", "INSERT INTO replies(job_id,ordinal,text,state) VALUES(1,0,'sent','submitted')", true},
		{"reminder unknown", "INSERT INTO reminders(source,user_id,text,due_utc,created_zone,status) SELECT source,'user','reminder',1,'UTC','unknown' FROM sources", true},
		{"other unknown", "UPDATE jobs SET state='unknown' WHERE id=2", true},
		{"reply", "INSERT INTO replies(job_id,ordinal,text,state) VALUES(1,0,'ambiguous','unknown')", true},
		{"note action", "INSERT INTO note_actions(job_id,source,action,note_id,state) SELECT 1,source,'share','note','failed' FROM sources", true},
		{"canceled unresolved tool", "INSERT INTO tool_operations(job_id,operation_id,arguments,state,result,resolution) VALUES(1,'effect','{}','unknown','','')", true},
		{"interactive deadline unresolved tool", "UPDATE jobs SET error='codex: interactive run: context deadline exceeded' WHERE id=1; INSERT INTO tool_operations(job_id,operation_id,arguments,state,result,resolution) VALUES(1,'effect','{}','dispatching','','')", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			source := Source{Name: "test", Sender: "sender", ChatGUID: "chat", ChatID: 1}
			if err = s.Initialize(ctx, source, 0, "gen"); err != nil {
				t.Fatal(err)
			}
			if err = s.ConfigureNotes(ctx, source, nil); err != nil {
				t.Fatal(err)
			}
			for i := int64(1); i <= 2; i++ {
				if _, err = s.Accept(ctx, source, Event{ID: i, GUID: string(rune('a' + i)), Text: "original request", CreatedAt: time.Now()}, "turn"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.db.Exec(`UPDATE jobs SET state='unknown',error='codex: context canceled' WHERE id=1; UPDATE sources SET paused=1,session='preserved';`); err != nil {
				t.Fatal(err)
			}
			if tc.mutation != "" {
				if _, err = s.db.Exec(tc.mutation); err != nil {
					t.Fatal(err)
				}
			}
			err = s.ResolveInterruptedAttempt(ctx, source, 1, "reviewed transcript; no external mutation")
			if (err != nil) != tc.reject {
				t.Fatalf("err=%v reject=%v", err, tc.reject)
			}
			var state, prompt, reason, session string
			var paused bool
			if err = s.db.QueryRow(`SELECT state,prompt,error FROM jobs WHERE id=1`).Scan(&state, &prompt, &reason); err != nil {
				t.Fatal(err)
			}
			if err = s.db.QueryRow(`SELECT session,paused FROM sources`).Scan(&session, &paused); err != nil {
				t.Fatal(err)
			}
			if session != "preserved" || prompt != "original request" {
				t.Fatal("request/session changed")
			}
			if tc.reject && strings.Contains(reason, "OUTSTANDING REQUEST") {
				t.Fatal("rejection mutated audit")
			}
			if !tc.reject && (state != "failed" || paused || !strings.Contains(reason, "OUTSTANDING REQUEST")) {
				t.Fatalf("%s %t %s", state, paused, reason)
			}
			if !tc.reject {
				var queued string
				if err = s.db.QueryRow(`SELECT state FROM jobs WHERE id=2`).Scan(&queued); err != nil {
					t.Fatal(err)
				}
				if queued != "queued" {
					t.Fatal(queued)
				}
			}
		})
	}
}
