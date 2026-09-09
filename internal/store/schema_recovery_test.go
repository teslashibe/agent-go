package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func rejectionTranscript(session, prompt string) []byte {
	failure := `{"status":400,"error":{"type":"invalid_request_error","code":"invalid_json_schema","param":"text.format.schema"}}`
	errorJSON, _ := json.Marshal(failure)
	promptJSON, _ := json.Marshal(prompt)
	return []byte(`{"type":"session_meta","payload":{"id":"` + session + `"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"t"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":` + string(promptJSON) + `}]}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t","last_agent_message":null,"error":{"message":` + string(errorJSON) + `}}}`)
}

func TestSchemaRejectionEvidence(t *testing.T) {
	good := rejectionTranscript("session", "request")
	for _, tc := range []struct {
		name   string
		data   []byte
		reject bool
	}{
		{"valid", good, false},
		{"wrong session", rejectionTranscript("other", "request"), true},
		{"wrong prompt", rejectionTranscript("session", "other"), true},
		{"incomplete", []byte(strings.Split(string(good), `{"type":"event_msg","payload":{"type":"task_complete"`)[0]), true},
		{"tool call", []byte(strings.Replace(string(good), `"role":"user"`, `"role":"assistant"`, 1)), true},
		{"later output", append(append([]byte{}, good...), []byte("\n"+`{"type":"response_item","payload":{"type":"function_call"}}`)...), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySchemaRejection(tc.data, "session", "request")
			if (err != nil) != tc.reject {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRetrySchemaRejectedAttempt(t *testing.T) {
	for _, mutation := range []string{"", "UPDATE sources SET source_invalid=1", "UPDATE jobs SET error='timeout'", "UPDATE jobs SET state='running'", "INSERT INTO note_actions(job_id,source,action,note_id,state) SELECT id,source,'create_note','','started' FROM jobs"} {
		t.Run(mutation, func(t *testing.T) {
			s, source, _ := reminderStore(t)
			if err := s.ConfigureNotes(context.Background(), source, nil); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			now := time.Now()
			if _, err := s.Accept(ctx, source, Event{ID: 1, GUID: "schema-request", Text: "request", CreatedAt: now, Sender: "first@example.test"}, "turn"); err != nil {
				t.Fatal(err)
			}
			job, _, err := s.ClaimNext(ctx, source, now)
			if err != nil || job == nil {
				t.Fatalf("claim %v", err)
			}
			_, err = s.db.Exec(`UPDATE sources SET session='session',paused=1; UPDATE jobs SET state='unknown',error='400 invalid_json_schema text.format.schema',ack_state='submitted'`)
			if err != nil {
				t.Fatal(err)
			}
			if mutation != "" {
				if _, err := s.db.Exec(mutation); err != nil {
					t.Fatal(err)
				}
			}
			err = s.RetrySchemaRejectedAttempt(ctx, source, job.ID, "reviewed native turn", rejectionTranscript("session", "request"))
			if mutation != "" {
				if err == nil {
					t.Fatal("unsafe retry accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RetrySchemaRejectedAttempt(ctx, source, job.ID, "duplicate", rejectionTranscript("session", "request")); err == nil {
				t.Fatal("duplicate retry accepted")
			}
			claimed, _, err := s.ClaimNext(ctx, source, time.Now())
			if err != nil || claimed == nil || claimed.ID != job.ID || claimed.Prompt != "request" || claimed.Ack || claimed.SessionID != "session" {
				t.Fatalf("claim=%+v err=%v", claimed, err)
			}
			var audit int
			if err := s.db.QueryRow(`SELECT count(*) FROM recovery_audit WHERE job_id=?`, job.ID).Scan(&audit); err != nil || audit != 2 {
				t.Fatalf("audit=%d err=%v", audit, err)
			}
		})
	}
}
