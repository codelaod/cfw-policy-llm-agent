package storage

import (
	"context"
	"encoding/json"
)

// SnapshotCandidateQuery reads only committed rows. It never starts or waits
// for collection. Source/config policy is checked against each actual producer.
type SnapshotCandidateQuery struct {
	ScopeRef      string
	SnapshotID    string
	Limit, Offset int
}

// Full inputs and producer configuration remain internal. A DataPlan exposes
// the explicit eligibility projection, never this broader candidate object.
type SnapshotCandidate struct {
	Snapshot       SnapshotRecord `json:"-"`
	Cloud          *CloudRecord   `json:"-"`
	ProducerStatus string         `json:"-"`
	DataStatus     string         `json:"-"`
}

func (s *Store) SnapshotCandidates(ctx context.Context, q SnapshotCandidateQuery) ([]SnapshotCandidate, error) {
	if q.ScopeRef == "" || pageBounds(q.Offset, q.Limit) != nil {
		return nil, ErrInputInvalid
	}
	query := "SELECT * FROM snapshots WHERE scope_ref=?"
	args := []any{q.ScopeRef}
	if q.SnapshotID != "" {
		query += " AND id=?"
		args = append(args, q.SnapshotID)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?"
	args = append(args, q.Limit, q.Offset)
	var rows []SnapshotRecord
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]SnapshotCandidate, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(row.Body), &row.Input) != nil {
			return nil, ErrInputInvalid
		}
		c := SnapshotCandidate{Snapshot: row}
		if row.SourceKind == "live_readonly" {
			var producer struct{ JobID, Status, DataStatus string }
			// raw_artifacts is intentionally not read. SnapshotID is a product field
			// inside CloudRecord.body; it is not a column on job_cloud_results.
			result := s.db.WithContext(ctx).Raw("SELECT r.job_id,j.status,COALESCE(d.data_status,'') AS data_status FROM job_cloud_results r JOIN e0_jobs j ON j.id=r.job_id LEFT JOIN prepared_audit_data d ON d.job_id=j.id AND d.snapshot_id=? WHERE json_extract(r.body,'$.snapshot_id')=? AND ((json_extract(r.body,'$.kind')='collect' AND j.kind='collect') OR (json_extract(r.body,'$.kind')='audit' AND j.kind='audit' AND d.job_id IS NOT NULL)) ORDER BY r.created_at DESC,r.id DESC LIMIT 1", row.ID, row.ID).Scan(&producer)
			if result.Error != nil {
				return nil, result.Error
			}
			if result.RowsAffected > 0 {
				// A malformed running producer is never a reusable committed product.
				if !terminal(producer.Status) {
					continue
				}
				cloud, err := jobCloudResult(s.db.WithContext(ctx), producer.JobID)
				if err != nil {
					return nil, err
				}
				if cloud == nil || cloud.Content.SnapshotID != row.ID || cloud.Content.ScopeRef != row.ScopeRef {
					return nil, ErrInputInvalid
				}
				c.Cloud = cloud
				c.ProducerStatus = producer.Status
				c.DataStatus = producer.DataStatus
			}
		}
		out = append(out, c)
	}
	return out, nil
}
