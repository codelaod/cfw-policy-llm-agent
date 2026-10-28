package app

import (
	"context"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

// ConfirmationService shares the same local draft/preview/confirm path for
// forms and conversation candidates. It holds no model or cloud capability.
type ConfirmationService struct{ Store *storage.Store }

func NewConfirmationService(store *storage.Store) *ConfirmationService {
	return &ConfirmationService{Store: store}
}
func (s *ConfirmationService) CreateDraft(ctx context.Context, request storage.CreateDraftRequest) (storage.ProfileDraft, error) {
	return s.Store.CreateProfileDraft(ctx, request)
}
func (s *ConfirmationService) GetDraft(ctx context.Context, id string) (storage.ProfileDraft, error) {
	return s.Store.GetProfileDraft(ctx, id)
}
func (s *ConfirmationService) UpdateDraft(ctx context.Context, id string, request storage.UpdateDraftRequest) (storage.ProfileDraft, error) {
	return s.Store.UpdateProfileDraft(ctx, id, request)
}
func (s *ConfirmationService) Preview(ctx context.Context, id string) (storage.ConfirmationPreview, error) {
	return s.Store.PreviewProfileDraft(ctx, id)
}
func (s *ConfirmationService) Confirm(ctx context.Context, id string, request storage.ConfirmDraftRequest, key string) (storage.ConfirmationResult, error) {
	return s.Store.ConfirmProfileDraft(ctx, id, request, key)
}
func (s *ConfirmationService) Reject(ctx context.Context, id string, expectedRevision int64, key string) (storage.ConfirmationResult, error) {
	return s.Store.RejectProfileDraft(ctx, id, expectedRevision, key)
}
func (s *ConfirmationService) FreezeInput(ctx context.Context, snapshotID string) (domain.Input, []storage.AssetHeadVersion, error) {
	return s.Store.FreezeInput(ctx, snapshotID)
}

type AssetView struct {
	Asset                  domain.Asset             `json:"asset"`
	DeclaredAsset          domain.Asset             `json:"declared_asset"`
	Head                   storage.AssetHeadVersion `json:"head"`
	LocalConfirmedSections []string                 `json:"local_confirmed_sections"`
	SourceKind             string                   `json:"source_kind"`
}
type AssetViewPage struct {
	Items  []AssetView `json:"items"`
	Offset int         `json:"offset"`
	Limit  int         `json:"limit"`
	Total  int         `json:"total"`
}

func (s *ConfirmationService) ReadAssets(ctx context.Context, snapshotID string, offset, limit int) (page AssetViewPage, err error) {
	page = AssetViewPage{Items: []AssetView{}, Offset: offset, Limit: limit}
	if offset < 0 || offset > 10000 || limit < 1 || limit > 100 {
		return page, storage.ErrInputInvalid
	}
	snapshot, err := s.Store.GetSnapshot(ctx, snapshotID)
	if err != nil {
		return page, err
	}
	in, heads, err := s.Store.FreezeInput(ctx, snapshotID)
	if err != nil {
		return page, err
	}
	byHead := map[string]storage.AssetHeadVersion{}
	for _, head := range heads {
		byHead[head.AssetID] = head
	}
	declared := map[string]domain.Asset{}
	for _, asset := range snapshot.Input.Assets {
		declared[asset.ID] = asset
	}
	page.Total = len(in.Assets)
	end := offset + limit
	if end > len(in.Assets) {
		end = len(in.Assets)
	}
	for i := offset; i < end; i++ {
		asset := in.Assets[i]
		head := byHead[asset.ID]
		sections := []string{}
		if head.ProfileVersionID != "" {
			sections = append(sections, "roles")
		}
		if head.IntentVersionID != "" {
			sections = append(sections, "access")
		}
		page.Items = append(page.Items, AssetView{Asset: asset, DeclaredAsset: declared[asset.ID], Head: head, LocalConfirmedSections: sections, SourceKind: snapshot.SourceKind})
	}
	return
}

func (s *ConfirmationService) ReadAssetContext(ctx context.Context, snapshotID, assetID string) (AssetView, error) {
	page, err := s.ReadAssets(ctx, snapshotID, 0, 100)
	if err != nil {
		return AssetView{}, err
	}
	for _, item := range page.Items {
		if item.Asset.ID == assetID {
			return item, nil
		}
	}
	return AssetView{}, storage.ErrNotFound
}
