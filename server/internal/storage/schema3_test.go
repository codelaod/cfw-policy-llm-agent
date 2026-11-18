package storage

import (
	"path/filepath"
	"testing"
)

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
