package storage

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

type RecheckRecord struct {
	ID         string          `json:"id"`
	OldAuditID string          `json:"old_audit_id"`
	NewAuditID string          `json:"new_audit_id"`
	CreatedAt  string          `json:"created_at"`
	Comparison json.RawMessage `json:"comparison" gorm:"-"`
	Body       string          `json:"-" gorm:"column:comparison"`
}

func (s *Store) SaveRecheck(ctx context.Context, oldID, newID string, comparison []byte) (record RecheckRecord, err error) {
	if len(comparison) == 0 || len(comparison) > 1<<20 || !json.Valid(comparison) {
		return record, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Raw("SELECT * FROM audit_rechecks WHERE old_audit_id=? AND new_audit_id=?", oldID, newID).Scan(&record)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected > 0 {
			if record.Body != string(comparison) {
				return ErrConflict
			}
			record.Comparison = json.RawMessage(record.Body)
			return nil
		}
		if _, e := auditRun(tx, oldID); e != nil {
			return e
		}
		if _, e := auditRun(tx, newID); e != nil {
			return e
		}
		id, e := opaqueID("recheck")
		if e != nil {
			return e
		}
		record = RecheckRecord{ID: id, OldAuditID: oldID, NewAuditID: newID, CreatedAt: now(), Body: string(comparison), Comparison: append(json.RawMessage(nil), comparison...)}
		return tx.Exec("INSERT INTO audit_rechecks(id,old_audit_id,new_audit_id,created_at,comparison) VALUES(?,?,?,?,?)", id, oldID, newID, record.CreatedAt, record.Body).Error
	})
	return
}

func (s *Store) GetRecheck(ctx context.Context, id string) (record RecheckRecord, err error) {
	r := s.db.WithContext(ctx).Raw("SELECT * FROM audit_rechecks WHERE id=?", id).Scan(&record)
	if r.Error != nil {
		return record, r.Error
	}
	if r.RowsAffected == 0 {
		return record, ErrNotFound
	}
	record.Comparison = json.RawMessage(record.Body)
	return
}
