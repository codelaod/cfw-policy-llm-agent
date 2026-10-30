package storage

import (
	"context"
	"gorm.io/gorm"
)

func validatePlanResourceGroup(tx *gorm.DB, p ExecutionPlan) error {
	if p.ResourceGroupVersionID == "" {
		return nil
	}
	v, e := resourceGroupVersion(tx, p.ResourceGroupVersionID)
	if e != nil {
		return e
	}
	heads, e := resourceGroupHeads(tx, v.GroupName, v.SourceRef, v.AccountRef)
	if e != nil {
		return e
	}
	if len(heads) != 1 || heads[0].VersionID != v.ID {
		return ErrPlanStale
	}
	if p.PreparedInput.Scope.AccountRef != v.AccountRef || p.DataSourceID != "local-offline" && p.DataSourceID != v.SourceRef {
		return ErrInputInvalid
	}
	resources := []PreparedResource{}
	switch {
	case p.Batch != nil:
		resources = append(resources, p.Batch.Resources...)
	case p.Preparation != nil:
		resources = append(resources, p.Preparation.Selected...)
	default:
		if g := p.PreparedInput.GatewayMetadata; g != nil {
			resources = append(resources, PreparedResource{ResourceID: g.ResourceID, Region: g.Region, ResourceType: g.ResourceType, AssetID: p.PreparedInput.Scope.GatewayRef})
		}
		for _, a := range p.PreparedInput.Assets {
			resources = append(resources, PreparedResource{ResourceID: a.ProviderResourceID, Region: a.Region, ResourceType: a.ResourceType, AssetID: a.ID})
		}
	}
	if len(resources) == 0 {
		return ErrInputInvalid
	}
	for _, r := range resources {
		matched := false
		for _, m := range v.Members {
			if r.ResourceID != "" {
				matched = matched || m.ResourceID == r.ResourceID && m.Region == r.Region && m.ResourceType == r.ResourceType
			} else {
				matched = matched || m.ResourceID == "" && m.LocalAssetID == r.AssetID && m.IdentityContext == p.PolicyScopeRef
			}
		}
		if !matched {
			return ErrInputInvalid
		}
	}
	return nil
}

func (s *Store) PendingResourceGroupPreview(ctx context.Context, conversation string) (*ResourceGroupPreview, error) {
	p, e := s.LatestResourceGroupPreview(ctx, conversation)
	if e != nil {
		return nil, e
	}
	var confirmed int64
	if e = s.db.WithContext(ctx).Raw("SELECT count(*) FROM resource_group_versions WHERE preview_id=?", p.ID).Scan(&confirmed).Error; e != nil {
		return nil, e
	}
	if confirmed > 0 {
		return nil, nil
	}
	return &p, nil
}
