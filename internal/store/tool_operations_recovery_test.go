package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestToolOperationReviewedRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{Name: "test", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if err = s.Initialize(ctx, source, 0, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state) VALUES(1,?,'one','test',0,'running')`, source.key()); err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 1, "original", `{}`); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if err = s.RecordToolProgress(ctx, source, 1, "original", 0, "verified first item"); err != nil {
		t.Fatal(err)
	}
	if err = s.DiscardRepliesAndResume(ctx, source, ""); !errors.Is(err, ErrBusy) {
		t.Fatal("running recovery", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err = s.ResetSource(ctx, source, "", 10, "new"); !errors.Is(err, ErrUncertain) {
		t.Fatal("reset bypass", err)
	}
	if err = s.DiscardRepliesAndResume(ctx, source, ""); err != nil {
		t.Fatal(err)
	}
	var state, resolution, progress, detail string
	if err = s.db.QueryRow(`SELECT state,resolution FROM tool_operations WHERE job_id=1`).Scan(&state, &resolution); err != nil || state != "unknown" || resolution != "abandoned" {
		t.Fatal(state, resolution, err)
	}
	if err = s.db.QueryRow(`SELECT progress,detail FROM tool_operation_reviews`).Scan(&progress, &detail); err != nil || progress != `[{"index":0,"result":"verified first item"}]` || detail == "" {
		t.Fatal(progress, detail, err)
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 1, "original", `{}`); claimed || err == nil {
		t.Fatal("replayed abandoned job", err)
	}
	if _, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state) VALUES(2,?,'two','test',0,'running')`, source.key()); err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 2, "new", `{}`); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if err = s.CompleteToolOperation(ctx, source, 2, "new", "read result"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE jobs SET state='completed' WHERE id=2`); err != nil {
		t.Fatal(err)
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
	if err = s.ResetSource(ctx, source, "fresh", 42, "verified-generation"); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	var generation, session string
	if err = s.db.QueryRow(`SELECT cursor,generation,session FROM sources`).Scan(&cursor, &generation, &session); err != nil || cursor != 42 || generation != "verified-generation" || session != "fresh" {
		t.Fatal(cursor, generation, session, err)
	}
	var count int
	for _, table := range []string{"jobs", "tool_operations", "tool_progress"} {
		if err = s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM tool_operation_reviews`).Scan(&count); err != nil || count != 1 {
		t.Fatal("lost review", count, err)
	}
}
