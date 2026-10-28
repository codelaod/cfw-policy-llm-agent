package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestE3BudgetSchemaRequiresExplicitMigrationAndKeepsExistingInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.sqlite")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = s.db.Raw("SELECT version FROM business_schema").Scan(&version).Error; err != nil || version != 3 {
		s.Close()
		t.Fatalf("budget schema is not enabled: version=%d err=%v", version, err)
	}
	in, _ := storedCase()
	saved, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"DROP TABLE outbound_attempts", "DROP TABLE budget_policies", "DROP TABLE business_schema", "CREATE TABLE business_schema(version INTEGER PRIMARY KEY CHECK(version=2))", "INSERT INTO business_schema VALUES(2)"} {
		if err = s.db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if stale, e := Open(path, false); e == nil {
		stale.Close()
		t.Fatal("opening an E2 database silently migrated its external admission schema")
	}
	s, err = Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.GetSnapshot(context.Background(), saved.ID)
	if err != nil || loaded.Input.SnapshotVersion != in.SnapshotVersion {
		t.Fatal("explicit schema upgrade changed frozen business input", err)
	}
	var policies, attempts int64
	if err = s.db.Raw("SELECT count(*) FROM budget_policies").Scan(&policies).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.db.Raw("SELECT count(*) FROM outbound_attempts").Scan(&attempts).Error; err != nil || policies != 0 || attempts != 0 {
		t.Fatal("migration manufactured authorization or usage", err)
	}
}

func TestE3StartupRejectsIncompleteAdmissionTablesWithoutImplicitRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incomplete.sqlite")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("DROP TABLE job_agent_results").Error; err != nil {
		t.Fatal(err)
	}
	s.Close()
	if stale, e := Open(path, false); e == nil {
		stale.Close()
		t.Fatal("startup accepted an incomplete admission schema")
	}
	s, err = Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path, false)
	if err != nil {
		t.Fatal("explicit migration did not repair the schema", err)
	}
	s.Close()
}
