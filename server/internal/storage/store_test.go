package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

func TestFileStoreWALTransactionsAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "e0.sqlite")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	if s.SQL.Stats().MaxOpenConnections != 8 {
		t.Fatal("ordinary shared pool must allow several connections")
	}
	// Keep four connections checked out so that PRAGMA verification actually
	// covers four independent physical connections rather than one reused handle.
	var release []func()
	for range 4 {
		conn, err := s.SQL.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		release = append(release, func() { conn.Close() })
		var fk, busy int
		var wal string
		if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err = conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err = conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&wal); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || busy != 5000 || wal != "wal" {
			t.Fatalf("invalid connection pragmas: fk=%d busy=%d wal=%s", fk, busy, wal)
		}
	}
	for _, close := range release {
		close()
	}
	if second, err := Open(path, false); err == nil {
		second.Close()
		t.Fatal("second process lock should fail")
	}
	// ORM map updates must preserve explicit false and empty JSON collections.
	if err = s.db.Exec("CREATE TABLE test_values(id INTEGER PRIMARY KEY, enabled BOOLEAN NOT NULL, items TEXT NOT NULL CHECK(json_valid(items)))").Error; err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("INSERT INTO test_values VALUES(1,1,'[1]')").Error; err != nil {
		t.Fatal(err)
	}
	if err = s.db.Table("test_values").Where("id=?", 1).Updates(map[string]any{"enabled": false, "items": "[]"}).Error; err != nil {
		t.Fatal(err)
	}
	var row struct {
		Enabled bool
		Items   string
	}
	if err = s.db.Table("test_values").Where("id=1").Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Enabled || row.Items != "[]" {
		t.Fatalf("zero values lost: %+v", row)
	}
	rollback := errors.New("rollback")
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE test_values SET enabled=1 WHERE id=1").Error; err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err = s.db.Table("test_values").Where("id=1").Scan(&row).Error; err != nil || row.Enabled {
		t.Fatal("transaction did not roll back")
	}
	if err = s.db.Exec("INSERT INTO e0_events VALUES('missing',1,1,'job_status','time','{}')").Error; err == nil {
		t.Fatal("foreign key not enforced")
	}
	j, _, err := s.Create(ctx, "conversation-a", "request-a")
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := s.Claim(ctx)
	if err != nil || !found || claimed.ID != j.ID {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, j.ID)
	if err != nil || got.Status != "interrupted" {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	p, _, err := s.Page(ctx, j.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Events) != 4 || p.Events[3].Type != "job_terminal" || p.LastSeq != got.ViewSeq {
		t.Fatalf("durable tail: %+v", p)
	}
	for i, e := range p.Events {
		if e.Seq != int64(i+1) || e.JobID != j.ID {
			t.Fatal("non-contiguous or mixed events")
		}
	}
	if err = s.Finish(ctx, j.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, j.ID)
	if got.Status != "interrupted" {
		t.Fatal("late success overwrote terminal")
	}
	_, _, err = s.Page(ctx, j.ID, got.ViewSeq+1, 100)
	if !errors.Is(err, ErrCursor) {
		t.Fatal("future cursor accepted")
	}
	// A stopped, closed database can be copied and restored in a separate private
	// directory. This deliberately is not an unsafe live main-file-only backup.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "restored.sqlite")
	if err = os.WriteFile(backup, content, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(backup, false)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.Get(ctx, j.ID)
	if err != nil || restored.Status != "interrupted" || restored.ViewSeq != got.ViewSeq {
		t.Fatalf("stopped backup restore: %+v %v", restored, err)
	}
}

func TestStateAndEventRollbackTogether(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "e0.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	j, _, err := s.Create(context.Background(), "c", "r")
	if err != nil {
		t.Fatal(err)
	}
	// Force only event insertion to fail. The state change must roll back too,
	// leaving nothing for SSE to publish as a successful terminal transition.
	if err = s.db.Exec("CREATE TRIGGER fail_event BEFORE INSERT ON e0_events BEGIN SELECT RAISE(ABORT,'test storage failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(context.Background(), j.ID, "completed"); err == nil {
		t.Fatal("save failure was swallowed")
	}
	got, err := s.Get(context.Background(), j.ID)
	if err != nil || got.Status != "queued" || got.ViewSeq != 1 {
		t.Fatalf("partial state commit: %+v %v", got, err)
	}
}
