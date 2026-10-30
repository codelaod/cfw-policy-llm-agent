package storage

import (
	"context"
	"encoding/json"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

// SaveCloudFailureObservations preserves controlled, already-read evidence.
// It cannot create a snapshot, seal input, publish rules or finish a job. The
// original accepted source and scope remain the only authority, including when
// cancellation prevents further computation or provider requests.
func (s *Store) SaveCloudFailureObservations(ctx context.Context, jobID string, scope domain.Scope, content CloudContent, code string) (record CloudRecord, err error) {
	kind := content.Kind
	if !enum(kind, "audit", "collect") || content.JobID != jobID || content.ProductReady || content.SnapshotID != "" || content.ScopeRef != scope.Ref || !validConfiguredScope(scope) || !cloudCode(code) {
		return record, ErrInputInvalid
	}
	content.Limitations = append(content.Limitations, "COLLECTION_ABORTED", "NO_SEALED_INPUT", code)
	content.Kind = "collect"
	content, err = normalizeCloud(content)
	if err != nil {
		return record, err
	}
	content.Kind = kind
	digest := requestDigest(struct {
		Scope            domain.Scope
		Content          CloudContent
		Source, Revision string
		Raw              json.RawMessage
	}{scope, content, content.SourceRef, content.ConfigRevision, content.RawArtifacts})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, jobID)
		if e != nil {
			return e
		}
		old, e := jobCloudResult(tx, jobID)
		if e != nil {
			return e
		}
		if old != nil {
			if old.RequestDigest != digest {
				return ErrConflict
			}
			record = *old
			return nil
		}
		if job.Kind != kind || !enum(job.Status, "running", "cancelling") || job.ResultRef != "" {
			return ErrJobTerminal
		}
		p, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		if p.Kind != kind || p.ConsumedJobID != jobID || p.ConversationID != job.ConversationID || p.BudgetRef != job.BudgetRef || p.DataSourceID != content.SourceRef || p.ConfigRevision != content.ConfigRevision || !p.EffectCeiling.AllowCloudReads || p.EffectCeiling.CloudHTTPAttempts < 1 {
			return ErrInputInvalid
		}
		if kind == "audit" {
			grant, e := validatePreparationGrant(tx, p)
			if e != nil {
				return e
			}
			if scope.AccountRef != grant.AccountRef || scope.ControlPlaneRef != grant.ControlPlaneRef || scope.Boundary != p.Preparation.Boundary || scope.Direction != p.Preparation.Direction || p.PolicyScopeRef != "" && scope != p.PreparedInput.Scope {
				return ErrInputInvalid
			}
		} else {
			if e = validateCloudPlan(tx, p); e != nil {
				return e
			}
			if scope != p.PreparedInput.Scope {
				return ErrInputInvalid
			}
		}
		id, e := opaqueID("cloud_result")
		if e != nil {
			return e
		}
		body, e := marshalBounded(content, 2<<20)
		if e != nil {
			return e
		}
		record = CloudRecord{ID: id, JobID: jobID, CreatedAt: now(), Content: content, Body: string(body), SourceRef: content.SourceRef, ConfigRevision: content.ConfigRevision, RequestDigest: digest}
		if e = tx.Exec("INSERT INTO job_cloud_results(id,job_id,created_at,source_ref,config_revision,body,raw_artifacts,request_digest) VALUES(?,?,?,?,?,?,?,?)", id, jobID, record.CreatedAt, content.SourceRef, content.ConfigRevision, record.Body, string(content.RawArtifacts), digest).Error; e != nil {
			return e
		}
		return appendEvent(tx, &job, "stage_changed", map[string]any{"phase": "L1", "stage": "cloud_result_saved", "cloud_result_ref": id, "collection_complete": false})
	})
	return record, err
}
