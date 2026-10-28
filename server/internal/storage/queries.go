package storage

import (
	"context"
	"encoding/json"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

func (s *Store) ListBusinessJobs(ctx context.Context, conversationID string, offset, limit int) (items []Job, err error) {
	items = []Job{}
	if err = pageBounds(offset, limit); err != nil {
		return
	}
	q := "SELECT * FROM e0_jobs WHERE kind<>'e0_probe'"
	args := []any{}
	if conversationID != "" {
		q += " AND conversation_id=?"
		args = append(args, conversationID)
	}
	q += " ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	err = s.db.WithContext(ctx).Raw(q, args...).Scan(&items).Error
	return
}

func (s *Store) ReadJobResult(ctx context.Context, id string) (job Job, run *AuditRecord, comparison *RecheckRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		job, e = get(tx, id)
		if e != nil {
			return e
		}
		if job.ResultRef == "" {
			return nil
		}
		value, e := auditRun(tx, job.ResultRef)
		if e != nil {
			return e
		}
		run = &value
		var saved RecheckRecord
		r := tx.Raw("SELECT * FROM audit_rechecks WHERE new_audit_id=? ORDER BY created_at,id LIMIT 1", value.ID).Scan(&saved)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected > 0 {
			saved.Comparison = json.RawMessage(saved.Body)
			comparison = &saved
		}
		return nil
	})
	return
}

type FindingView struct {
	FindingRecord
	SnapshotID     string `json:"snapshot_id"`
	SourceKind     string `json:"source_kind"`
	ReviewRevision int64  `json:"review_revision"`
	Plan           string `json:"plan"`
}

const findingViewQuery = `SELECT findings.*, audit_runs.snapshot_id, snapshots.source_kind, review_heads.revision AS review_revision, review_heads.plan FROM findings JOIN audit_runs ON findings.audit_id=audit_runs.id JOIN snapshots ON snapshots.id=audit_runs.snapshot_id JOIN review_heads ON review_heads.finding_id=findings.id`

func (s *Store) ListFindingViews(ctx context.Context, auditID string, offset, limit int) (items []FindingView, err error) {
	items = []FindingView{}
	if err = pageBounds(offset, limit); err != nil {
		return
	}
	q := findingViewQuery
	args := []any{}
	if auditID != "" {
		q += " WHERE findings.audit_id=?"
		args = append(args, auditID)
	}
	q += " ORDER BY audit_runs.created_at DESC,audit_runs.id DESC,findings.id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	err = s.db.WithContext(ctx).Raw(q, args...).Scan(&items).Error
	if err != nil {
		return
	}
	for i := range items {
		if err = json.Unmarshal([]byte(items[i].Body), &items[i].Finding); err != nil {
			return
		}
	}
	return
}

// ReadFindingView captures the fixed audit input and review head together.
func (s *Store) ReadFindingView(ctx context.Context, id string) (view FindingView, run AuditRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Raw(findingViewQuery+" WHERE findings.id=?", id).Scan(&view)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrNotFound
		}
		if e := json.Unmarshal([]byte(view.Body), &view.Finding); e != nil {
			return e
		}
		var e error
		run, e = auditRun(tx, view.AuditID)
		return e
	})
	return
}

type ReportView struct {
	ReportRecord
	JobID string `json:"job_id,omitempty"`
}

func (s *Store) JobForAudit(ctx context.Context, id string) (job Job, found bool, err error) {
	r := s.db.WithContext(ctx).Raw("SELECT * FROM e0_jobs WHERE result_ref=? ORDER BY created_at,id LIMIT 1", id).Scan(&job)
	return job, r.RowsAffected == 1, r.Error
}

func (s *Store) ReportView(ctx context.Context, id string) (view ReportView, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		view.ReportRecord, e = reportRecord(tx, id)
		if e != nil {
			return e
		}
		return tx.Raw("SELECT id FROM e0_jobs WHERE result_ref=? ORDER BY created_at,id LIMIT 1", view.AuditID).Scan(&view.JobID).Error
	})
	return
}

func (s *Store) ReportPage(ctx context.Context, auditID string, offset, limit int) (items []ReportView, err error) {
	items = []ReportView{}
	if err = pageBounds(offset, limit); err != nil {
		return
	}
	q := `SELECT reports.*,COALESCE((SELECT id FROM e0_jobs WHERE result_ref=reports.audit_id ORDER BY created_at,id LIMIT 1),'') AS job_id FROM reports`
	args := []any{}
	if auditID != "" {
		q += " WHERE audit_id=?"
		args = append(args, auditID)
	}
	q += " ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	err = s.db.WithContext(ctx).Raw(q, args...).Scan(&items).Error
	if err != nil {
		return
	}
	for i := range items {
		if err = json.Unmarshal([]byte(items[i].ReviewJSON), &items[i].Reviews); err != nil {
			return
		}
	}
	return
}

func (s *Store) PolicyScopes(ctx context.Context, offset, limit int) (items []domain.Scope, err error) {
	items = []domain.Scope{}
	if err = pageBounds(offset, limit); err != nil {
		return
	}
	var rows []struct{ Body string }
	err = s.db.WithContext(ctx).Raw(`SELECT body FROM (SELECT scope_ref,json_extract(body,'$.scope') AS body FROM snapshots s WHERE id=(SELECT id FROM snapshots n WHERE n.scope_ref=s.scope_ref ORDER BY created_at DESC,id DESC LIMIT 1) UNION ALL SELECT scope_ref,body FROM configured_scope_catalog c WHERE active=1 AND NOT EXISTS(SELECT 1 FROM snapshots s WHERE s.scope_ref=c.scope_ref)) ORDER BY scope_ref LIMIT ? OFFSET ?`, limit, offset).Scan(&rows).Error
	if err != nil {
		return
	}
	for _, row := range rows {
		var scope domain.Scope
		if err = json.Unmarshal([]byte(row.Body), &scope); err != nil {
			return
		}
		items = append(items, scope)
	}
	return
}
