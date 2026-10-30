package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"

	"gorm.io/gorm"
)

type DraftCurrentBase struct {
	DraftID        string             `json:"draft_id"`
	DraftRevision  int64              `json:"draft_revision"`
	SnapshotID     string             `json:"snapshot_id"`
	PolicyScopeRef string             `json:"policy_scope_ref"`
	Changes        []AssetDraftChange `json:"changes"`
	BaseRevisions  []AssetHeadVersion `json:"base_revisions"`
}
type RebaseDraftRequest struct {
	ExpectedDraftRevision int64              `json:"expected_draft_revision"`
	ExpectedBaseRevisions []AssetHeadVersion `json:"expected_base_revisions"`
	Changes               []AssetDraftChange `json:"changes"`
}

func quietDraftDeclaration() *bool { value := false; return &value }

func currentDraftBase(tx *gorm.DB, draft ProfileDraft) (base DraftCurrentBase, err error) {
	in, heads, err := freezeInput(tx, draft.SnapshotID)
	if err != nil {
		return base, err
	}
	if canonicalBusinessScope(in.Scope) != draft.ScopeKey || in.Scope.Ref != draft.PolicyScopeRef {
		return base, ErrInputInvalid
	}
	base = DraftCurrentBase{DraftID: draft.ID, DraftRevision: draft.Revision, SnapshotID: draft.SnapshotID, PolicyScopeRef: draft.PolicyScopeRef, Changes: []AssetDraftChange{}, BaseRevisions: []AssetHeadVersion{}}
	for _, original := range draft.BaseRevisions {
		asset := selectedAsset(&in, original.AssetID)
		if asset == nil {
			return base, ErrInputInvalid
		}
		change := initialDraftChange(*asset)
		change.Access.Exceptions = exceptionsForAsset(in, original.AssetID)
		for i := range change.Roles {
			change.Roles[i].Declared = quietDraftDeclaration()
		}
		change.Access.AllowedDeclared, change.Access.ExceptionsDeclared = quietDraftDeclaration(), quietDraftDeclaration()
		for i := range change.Access.Services {
			change.Access.Services[i].AllowedDeclared, change.Access.Services[i].RequiredDeclared = quietDraftDeclaration(), quietDraftDeclaration()
		}
		for i := range change.Access.Exceptions {
			change.Access.Exceptions[i].Confirmed = false
		}
		base.Changes = append(base.Changes, change)
		for _, head := range heads {
			if head.AssetID == original.AssetID {
				base.BaseRevisions = append(base.BaseRevisions, head)
			}
		}
	}
	if len(base.BaseRevisions) != len(draft.BaseRevisions) {
		return base, ErrInputInvalid
	}
	sort.Slice(base.BaseRevisions, func(i, j int) bool { return base.BaseRevisions[i].AssetID < base.BaseRevisions[j].AssetID })
	return base, nil
}

func (s *Store) ProfileDraftCurrentBase(ctx context.Context, id string) (base DraftCurrentBase, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		draft, e := getProfileDraft(tx, id)
		if e != nil {
			return e
		}
		base, e = currentDraftBase(tx, draft)
		return e
	})
	return
}

// Rebase is an explicit edit after the caller has inspected current heads. It
// retains the same immutable target and conversation mapping, invalidates the
// preview and confirms no section. Receipt replay precedes all current checks.
func (s *Store) RebaseProfileDraft(ctx context.Context, id string, req RebaseDraftRequest, key string) (draft ProfileDraft, err error) {
	if !validKey(key) || req.ExpectedDraftRevision < 1 || len(req.ExpectedBaseRevisions) == 0 {
		return draft, ErrInputInvalid
	}
	raw, err := marshalBounded(req, 2<<20)
	if err != nil || json.Unmarshal(raw, &req) != nil {
		return draft, ErrInputInvalid
	}
	sort.Slice(req.ExpectedBaseRevisions, func(i, j int) bool {
		return req.ExpectedBaseRevisions[i].AssetID < req.ExpectedBaseRevisions[j].AssetID
	})
	digest := requestDigest(struct {
		DraftID string
		Request RebaseDraftRequest
	}{id, req})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old struct{ DraftID, RequestDigest, ResultJSON string }
		r := tx.Raw("SELECT draft_id,request_digest,result_json FROM profile_draft_rebase_requests WHERE request_key=?", key).Scan(&old)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 0 {
			if old.DraftID != id || old.RequestDigest != digest {
				return ErrConflict
			}
			return json.Unmarshal([]byte(old.ResultJSON), &draft)
		}
		current, e := getProfileDraft(tx, id)
		if e != nil {
			return e
		}
		if current.Revision != req.ExpectedDraftRevision || !enum(current.Status, "draft", "previewed") {
			return ErrVersionConflict
		}
		base, e := currentDraftBase(tx, current)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(base.BaseRevisions, req.ExpectedBaseRevisions) {
			return ErrVersionConflict
		}
		in, _, e := freezeInput(tx, current.SnapshotID)
		if e != nil {
			return e
		}
		current.BaseRevisions = base.BaseRevisions
		changes, e := normalizeDraftChanges(in, current, req.Changes)
		if e != nil {
			return e
		}
		body, e := marshalBounded(changes, 1<<20)
		if e != nil {
			return e
		}
		heads, e := marshalBounded(base.BaseRevisions, 1<<20)
		if e != nil {
			return e
		}
		r = tx.Exec("UPDATE profile_drafts SET body=?,base_json=?,revision=revision+1,status='draft',preview_id=NULL,updated_at=? WHERE id=? AND revision=?", string(body), string(heads), now(), id, req.ExpectedDraftRevision)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrVersionConflict
		}
		draft, e = getProfileDraft(tx, id)
		if e != nil {
			return e
		}
		response, e := marshalBounded(draft, 2<<20)
		if e != nil {
			return e
		}
		return tx.Exec("INSERT INTO profile_draft_rebase_requests(request_key,draft_id,request_digest,result_json) VALUES(?,?,?,?)", key, id, digest, string(response)).Error
	})
	return
}
