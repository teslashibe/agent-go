package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRetryBindingRejectedAttempt(t *testing.T) {
	for _, tc := range []struct {
		name, mutation string
		reject         bool
	}{
		{"success", "", false},
		{"wrong error", "UPDATE jobs SET error='codex: context canceled' WHERE id=1", true},
		{"wrong source", "", true},
		{"running", "UPDATE jobs SET state='running' WHERE id=1", true},
		{"not paused", "UPDATE sources SET paused=0", true},
		{"invalid", "UPDATE sources SET source_invalid=1", true},
		{"other running", "UPDATE jobs SET state='running' WHERE id=2", true},
		{"other unknown", "UPDATE jobs SET state='unknown' WHERE id=2", true},
		{"submitted reply", "INSERT INTO replies(job_id,ordinal,text,state) VALUES(1,0,'sent','submitted')", true},
		{"other reply", "INSERT INTO replies(job_id,ordinal,text,state) VALUES(2,0,'sent','unknown')", true},
		{"tool completed", "INSERT INTO tool_operations(job_id,operation_id,arguments,state) VALUES(1,'x','{}','completed')", true},
		{"other tool", "INSERT INTO tool_operations(job_id,operation_id,arguments,state) VALUES(2,'x','{}','unknown')", true},
		{"abandoned sibling", "INSERT INTO tool_operations(job_id,operation_id,arguments,state,resolution,result) VALUES(2,'x','{}','unknown','abandoned','preserved'); INSERT INTO native_note_claims(job_id,operation_id) VALUES(2,'x'); INSERT INTO tool_operation_reviews(source,job_id,operation_id,arguments,original_state,result,progress,detail) SELECT source,2,'x','{}','unknown','preserved','[]','human-reviewed abandonment' FROM sources", false},
		{"abandoned target", "INSERT INTO tool_operations(job_id,operation_id,arguments,state,resolution) VALUES(1,'x','{}','unknown','abandoned')", true},
		{"sibling note started", "INSERT INTO note_actions(job_id,source,action,note_id,state) SELECT 2,source,'share','note','started' FROM sources", true},
		{"sibling note created", "INSERT INTO note_actions(job_id,source,action,note_id,state) SELECT 2,source,'share','note','created_unshared' FROM sources", true},
		{"sibling note sharing", "INSERT INTO note_actions(job_id,source,action,note_id,state) SELECT 2,source,'share','note','sharing_unverified' FROM sources", true},
		{"sibling note failed", "INSERT INTO note_actions(job_id,source,action,note_id,state) SELECT 2,source,'share','note','sharing_failed' FROM sources", false},
		{"note", "INSERT INTO note_actions(job_id,source,action,note_id,state) SELECT 1,source,'share','note','failed' FROM sources", true},
		{"reminder", "INSERT INTO reminders(source,user_id,text,due_utc,created_zone,status) SELECT source,'u','r',1,'UTC','dispatching' FROM sources", true},
		{"rollback", "CREATE TRIGGER reject_unpause BEFORE UPDATE OF paused ON sources BEGIN SELECT RAISE(ABORT,'injected'); END", true},
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
			if _, err = s.db.Exec("UPDATE jobs SET state='unknown',error=? WHERE id=1", bindingRejection); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("UPDATE sources SET paused=1,session='preserved'"); err != nil {
				t.Fatal(err)
			}
			if tc.mutation != "" {
				if _, err = s.db.Exec(tc.mutation); err != nil {
					t.Fatal(err)
				}
			}
			var before, after string
			query := `SELECT json_array(prompt,sender,guid,ack_state,ack_reaction,(SELECT session FROM sources LIMIT 1)) FROM jobs WHERE id=1`
			if err = s.db.QueryRow(query).Scan(&before); err != nil {
				t.Fatal(err)
			}
			target := source
			if tc.name == "wrong source" {
				target.Name = "other"
			}
			var evidenceBefore, evidenceAfter string
			evidenceQuery := `SELECT json_array((SELECT json_group_array(json_array(job_id,operation_id,arguments,state,result,resolution)) FROM tool_operations),(SELECT json_group_array(json_array(job_id,operation_id)) FROM native_note_claims),(SELECT json_group_array(json_array(id,source,job_id,operation_id,arguments,original_state,result,progress,detail,created_at)) FROM tool_operation_reviews))`
			if err = s.db.QueryRow(evidenceQuery).Scan(&evidenceBefore); err != nil {
				t.Fatal(err)
			}
			err = s.RetryBindingRejectedAttempt(ctx, target, 1, "verified prelaunch failure and repaired binding")
			if scanErr := s.db.QueryRow(evidenceQuery).Scan(&evidenceAfter); scanErr != nil {
				t.Fatal(scanErr)
			}
			if evidenceBefore != evidenceAfter {
				t.Fatal("historical operation/claim/review evidence changed")
			}
			if (err != nil) != tc.reject {
				t.Fatalf("err=%v reject=%v", err, tc.reject)
			}
			if err = s.db.QueryRow(query).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("request/session/ack changed")
			}
			var state, failure string
			var paused bool
			if err = s.db.QueryRow("SELECT state,error FROM jobs WHERE id=1").Scan(&state, &failure); err != nil {
				t.Fatal(err)
			}
			if err = s.db.QueryRow("SELECT paused FROM sources").Scan(&paused); err != nil {
				t.Fatal(err)
			}
			if !tc.reject {
				if state != "queued" || paused {
					t.Fatalf("state=%s paused=%v", state, paused)
				}
				if err = s.RetryBindingRejectedAttempt(ctx, source, 1, "repeat"); err == nil {
					t.Fatal("repeat succeeded")
				}
				var count int
				if err = s.db.QueryRow("SELECT count(*) FROM recovery_audit WHERE job_id=1").Scan(&count); err != nil || count != 2 {
					t.Fatalf("audit=%d err=%v", count, err)
				}
			}
			if tc.name == "rollback" && (state != "unknown" || !paused || failure != bindingRejection) {
				t.Fatal("transaction partially applied")
			}
		})
	}
}
