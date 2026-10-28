package storage

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

// JobWorkspace is one authoritative read snapshot. The snapshot reference comes
// from the accepted plan; effective input remains exclusively the sealed row.
type JobWorkspace struct {
	Job             Job
	SnapshotID      string
	InputSealed     bool
	Audit           *AuditRecord
	Recheck         *RecheckRecord
	AgentResult     *AgentRecord
	AttemptUsage    AttemptUsage
	ReportPublished bool
	ReportRef       string
}

func (s *Store) ReadJobWorkspace(ctx context.Context, id string) (view JobWorkspace, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		view.Job, e = get(tx, id)
		if e != nil {
			return e
		}
		if !enum(view.Job.Kind, "audit", "assistant_turn", "collect", "connection_check") {
			return ErrInputInvalid
		}
		plan, e := planRecord(tx, view.Job.PlanID)
		if e != nil {
			return e
		}
		view.SnapshotID = plan.SnapshotID
		if e = tx.Raw("SELECT sealed_input IS NOT NULL FROM e0_jobs WHERE id=?", id).Scan(&view.InputSealed).Error; e != nil {
			return e
		}
		if view.Job.ResultRef != "" {
			audit, e := auditRun(tx, view.Job.ResultRef)
			if e != nil {
				return e
			}
			view.Audit = &audit
			var saved RecheckRecord
			r := tx.Raw("SELECT * FROM audit_rechecks WHERE new_audit_id=? ORDER BY created_at,id LIMIT 1", audit.ID).Scan(&saved)
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected > 0 {
				saved.Comparison = json.RawMessage(saved.Body)
				view.Recheck = &saved
			}
		}
		view.AgentResult, e = jobAgentResult(tx, id)
		if e != nil {
			return e
		}
		// A later manual export has no task execution event. Only this job's
		// committed publication can mark its original report stage complete.
		var publication struct{ ReportRef string }
		r := tx.Raw("SELECT json_extract(payload,'$.report_ref') AS report_ref FROM e0_events WHERE job_id=? AND type='report_available' AND seq<=? ORDER BY seq DESC LIMIT 1", id, view.Job.ViewSeq).Scan(&publication)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected > 0 && publication.ReportRef != "" {
			view.ReportPublished = true
			view.ReportRef = publication.ReportRef
		}
		view.AttemptUsage, e = budgetUsage(tx, view.Job.BudgetRef, "", id)
		return e
	})
	return
}
