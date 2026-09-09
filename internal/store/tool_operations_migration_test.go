package store

import (
	"context"
	"testing"
)

func TestToolOperationMigrationPreservesLegacyProgress(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state) VALUES(99,?,'legacy','test',0,'completed');`, source.key()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE tool_operations DROP COLUMN resolution;
 INSERT INTO tool_operations(job_id,operation_id,arguments,state,result) VALUES(99,'read','{}','completed','original');
 INSERT INTO tool_progress VALUES(99,'read',0,'progress');`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateToolOperations(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateToolOperations(); err != nil {
		t.Fatal(err)
	}
	var result, progress, resolution string
	if err := s.db.QueryRow(`SELECT o.result,p.result,o.resolution FROM tool_operations o JOIN tool_progress p USING(job_id,operation_id)`).Scan(&result, &progress, &resolution); err != nil || result != "original" || progress != "progress" || resolution != "" {
		t.Fatal(result, progress, resolution, err)
	}
	if err := s.ResetSource(ctx, source, "", 123, "read-generation"); err != nil {
		t.Fatal(err)
	}
}
