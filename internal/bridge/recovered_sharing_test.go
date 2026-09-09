package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
)

type recoveredNotes struct {
	fakeNotes
	shared       bool
	verifies     int
	participants []string
	verify       func() error
}

func (n *recoveredNotes) List(context.Context) ([]notes.Note, error) {
	return []notes.Note{{ID: "created-id", Name: "Fixture", Shared: n.shared}}, nil
}

func (n *recoveredNotes) VerifyParticipants(_ context.Context, id string, participants []string) error {
	if id != "created-id" {
		return errors.New("wrong note")
	}
	n.verifies++
	n.participants = participants
	if n.verify != nil {
		return n.verify()
	}
	return nil
}

func TestRecoveredSharingRequiresReviewAndFreshMembership(t *testing.T) {
	for _, test := range []struct {
		name          string
		mutate        string
		shared        bool
		verifyFail    bool
		invalidateJob bool
		wantVerify    int
		wantRead      int
	}{
		{name: "verified", shared: true, wantVerify: 1, wantRead: 1},
		{name: "wrong participants", shared: true, verifyFail: true, wantVerify: 1},
		{name: "not shared"},
		{name: "not reviewed", shared: true, mutate: "UPDATE tool_operations SET resolution='' WHERE job_id=1"},
		{name: "old job unresolved", shared: true, mutate: "UPDATE jobs SET state='unknown' WHERE id=1"},
		{name: "wrong chat scope", shared: true, mutate: "UPDATE note_actions SET note_id='another-id'"},
		{name: "job changed after native verification", shared: true, invalidateJob: true, wantVerify: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			source := store.Source{Name: "fixture", ChatID: 1, ChatGUID: "any;+;fixture", Group: true, AllowedSenders: []string{"+15555501001", "+15555501002"}}
			if err := s.Initialize(ctx, source, 0, "generation"); err != nil {
				t.Fatal(err)
			}
			if err := s.ConfigureNotes(ctx, source, nil); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(source)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, err = db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state,error,sender,ack_state)
 VALUES(1,?,'old','original request',1,'failed','original uncertain sharing',?,'submitted'),
 (2,?,'new','read existing fixture',2,'running','',?,'submitted');
 INSERT INTO tool_operations(job_id,operation_id,arguments,state,result,resolution)
 VALUES(1,'create','{}','unknown','original uncertainty','abandoned');
 INSERT INTO native_note_claims(job_id,operation_id) VALUES(1,'create');
 INSERT INTO note_actions(job_id,source,action,note_id,state,error)
 VALUES(1,?,'create_shared_note','created-id','unknown','original note error');
 INSERT INTO note_scopes(source,id,title,state) VALUES(?,'created-id','Fixture','sharing_unverified');
 CREATE TABLE recovery_audit(id INTEGER PRIMARY KEY,job_id INTEGER NOT NULL,detail TEXT NOT NULL);`,
				string(raw), source.AllowedSenders[0], string(raw), source.AllowedSenders[0], string(raw), string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != "" {
				if _, err := db.Exec(test.mutate); err != nil {
					t.Fatal(err)
				}
			}
			client := &recoveredNotes{shared: test.shared}
			client.verify = func() error {
				if test.verifyFail {
					return errors.New("membership mismatch")
				}
				if test.invalidateJob {
					_, err := db.Exec("UPDATE jobs SET state='failed' WHERE id=2")
					return err
				}
				return nil
			}
			b, err := New(s, &fakeRunner{}, &fakeMessenger{}, Config{Source: source, StructuredActions: true})
			if err != nil {
				t.Fatal(err)
			}
			b.notes = client
			turn := newNotesTurn(ctx, b, &store.Job{ID: 2, GUID: "new", Sender: source.AllowedSenders[0]})
			result := rpc(t, turn, "read_note", `{"operation_id":"read","note_id":"created-id"}`)
			if client.verifies != test.wantVerify || client.reads != test.wantRead || client.creates != 0 || client.adds != 0 {
				t.Fatalf("verify=%d reads=%d result=%s", client.verifies, client.reads, result)
			}
			if client.verifies > 0 && !reflect.DeepEqual(client.participants, source.AllowedSenders) {
				t.Fatal("participant authority changed")
			}
			var originalError, originalOutcome, resolution string
			if err := db.QueryRow("SELECT j.error,o.result,o.resolution FROM jobs j JOIN tool_operations o ON o.job_id=j.id WHERE j.id=1").Scan(&originalError, &originalOutcome, &resolution); err != nil {
				t.Fatal(err)
			}
			if originalError != "original uncertain sharing" || originalOutcome != "original uncertainty" {
				t.Fatal("old evidence changed")
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM recovery_audit").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != test.wantRead {
				t.Fatalf("unexpected authorization audit count %d", count)
			}
		})
	}
}
