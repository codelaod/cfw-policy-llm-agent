package storage

import (
	"cfw-policy-llm-agent/server/internal/domain"
	"context"
	"encoding/json"
)

// SnapshotResources returns source-qualified membership bound when the artifact
// was saved. Current directory labels never alter that historical membership.
func (s *Store) SnapshotResources(ctx context.Context, id string) (items []domain.Resource, err error) {
	if !attemptAlias(id) {
		return nil, ErrInputInvalid
	}
	if _, e := s.GetSnapshot(ctx, id); e != nil {
		return nil, e
	}
	var rows []struct{ Body, AssetID string }
	if err = s.db.WithContext(ctx).Raw("SELECT c.body,b.asset_id FROM resource_snapshot_bindings b JOIN resource_catalog c ON c.id=b.resource_ref WHERE b.snapshot_id=? ORDER BY b.asset_id,b.resource_ref", id).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items = []domain.Resource{}
	for _, row := range rows {
		var r domain.Resource
		if json.Unmarshal([]byte(row.Body), &r) != nil {
			return nil, ErrInputInvalid
		}
		if row.AssetID != "" {
			r.LocalAssetID = row.AssetID
		}
		items = append(items, r)
	}
	return
}
