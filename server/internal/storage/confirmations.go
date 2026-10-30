package storage

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

type RoleChange struct {
	Declared  *bool              `json:"declared,omitempty"`
	ServiceID string             `json:"service_id"`
	Role      string             `json:"role"`
	Protocol  string             `json:"protocol"`
	Ports     []domain.PortRange `json:"ports"`
}
type ServiceAccessChange struct {
	AllowedDeclared   *bool           `json:"allowed_declared,omitempty"`
	RequiredDeclared  *bool           `json:"required_declared,omitempty"`
	ServiceID         string          `json:"service_id"`
	PublicAccess      string          `json:"public_access"`
	AllowedComplete   bool            `json:"allowed_complete"`
	DenyAll           bool            `json:"deny_all"`
	Allowed           []domain.Access `json:"allowed"`
	RequiredConfirmed bool            `json:"required_confirmed"`
	Required          []domain.Access `json:"required"`
}
type AssetAccessChange struct {
	AllowedDeclared    *bool                 `json:"allowed_declared,omitempty"`
	ExceptionsDeclared *bool                 `json:"exceptions_declared,omitempty"`
	AllowedComplete    bool                  `json:"allowed_complete"`
	DenyAll            bool                  `json:"deny_all"`
	Allowed            []domain.Access       `json:"allowed"`
	Services           []ServiceAccessChange `json:"services"`
	Exceptions         []domain.Exception    `json:"exceptions"`
}
type AssetDraftChange struct {
	AssetID string             `json:"asset_id"`
	Roles   []RoleChange       `json:"roles"`
	Access  *AssetAccessChange `json:"access,omitempty"`
}
type AssetHeadVersion struct {
	AssetID          string `json:"asset_id" gorm:"column:asset_key"`
	Revision         int64  `json:"revision"`
	ProfileVersionID string `json:"profile_version_id"`
	IntentVersionID  string `json:"intent_version_id"`
}
type CreateDraftRequest struct {
	SnapshotID string   `json:"snapshot_id"`
	AssetIDs   []string `json:"asset_ids"`
}
type UpdateDraftRequest struct {
	ExpectedDraftRevision int64              `json:"expected_draft_revision"`
	Changes               []AssetDraftChange `json:"changes"`
}
type ProfileDraft struct {
	ID             string             `json:"id"`
	SnapshotID     string             `json:"snapshot_id"`
	PolicyScopeRef string             `json:"policy_scope_ref"`
	Revision       int64              `json:"revision"`
	Status         string             `json:"status"`
	Changes        []AssetDraftChange `json:"changes" gorm:"-"`
	BaseRevisions  []AssetHeadVersion `json:"base_revisions" gorm:"-"`
	PreviewID      string             `json:"preview_id,omitempty"`
	ConfirmationID string             `json:"confirmation_id,omitempty"`
	CreatedAt      string             `json:"created_at"`
	UpdatedAt      string             `json:"updated_at"`
	ScopeKey       string             `json:"-"`
	Body           string             `json:"-"`
	BaseJSON       string             `json:"-"`
}
type ConfirmationDiff struct {
	AssetID string          `json:"asset_id"`
	Section string          `json:"section"`
	Before  json.RawMessage `json:"before"`
	After   json.RawMessage `json:"after"`
}
type DraftValidationError struct {
	AssetID string `json:"asset_id"`
	Section string `json:"section"`
	Code    string `json:"code"`
}
type ConfirmationPreview struct {
	ID                  string                 `json:"preview_id"`
	DraftID             string                 `json:"draft_id"`
	DraftRevision       int64                  `json:"draft_revision"`
	BaseRevisions       []AssetHeadVersion     `json:"base_revisions"`
	Diff                []ConfirmationDiff     `json:"diff"`
	ConfirmableSections []string               `json:"confirmable_sections"`
	SectionDependencies map[string][]string    `json:"section_dependencies,omitempty"`
	ValidationErrors    []DraftValidationError `json:"validation_errors"`
	CreatedAt           string                 `json:"created_at"`
}
type ConfirmDraftRequest struct {
	PreviewID             string   `json:"preview_id"`
	ExpectedDraftRevision int64    `json:"expected_draft_revision"`
	ConfirmedSections     []string `json:"confirmed_sections"`
}
type ConfirmationResult struct {
	ActionID string             `json:"action_id"`
	DraftID  string             `json:"draft_id"`
	Status   string             `json:"status"`
	Revision int64              `json:"revision"`
	Heads    []AssetHeadVersion `json:"heads"`
	SavedAt  string             `json:"saved_at"`
}
type profileService struct {
	ID         string             `json:"id"`
	Role       string             `json:"role"`
	RoleStatus string             `json:"role_status"`
	Protocol   string             `json:"protocol"`
	Ports      []domain.PortRange `json:"ports"`
}
type profilePayload struct {
	Services []profileService `json:"services"`
}
type intentPayload struct {
	AllowedConfirmed bool               `json:"allowed_confirmed"`
	AllowedComplete  bool               `json:"allowed_complete"`
	DenyAll          bool               `json:"deny_all"`
	Allowed          []domain.Access    `json:"allowed"`
	Services         []intentService    `json:"services"`
	Exceptions       []domain.Exception `json:"exceptions"`
}
type intentService struct {
	ID                string          `json:"id"`
	PublicAccess      string          `json:"public_access"`
	AllowedConfirmed  bool            `json:"allowed_confirmed"`
	AllowedComplete   bool            `json:"allowed_complete"`
	DenyAll           bool            `json:"deny_all"`
	Allowed           []domain.Access `json:"allowed"`
	RequiredConfirmed bool            `json:"required_confirmed"`
	Required          []domain.Access `json:"required"`
}

func canonicalBusinessScope(scope domain.Scope) string { scope.Ref = ""; return requestDigest(scope) }
func cloneConfirmationInput(in domain.Input) (domain.Input, error) {
	var out domain.Input
	data, err := marshalBounded(in, 5<<20)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}
func selectedAsset(in *domain.Input, id string) *domain.Asset {
	for i := range in.Assets {
		if in.Assets[i].ID == id {
			return &in.Assets[i]
		}
	}
	return nil
}
func selectedService(asset *domain.Asset, id string) *domain.Service {
	for i := range asset.Services {
		if asset.Services[i].ID == id {
			return &asset.Services[i]
		}
	}
	return nil
}
func loadAssetHead(tx *gorm.DB, scopeKey, assetID string) (head AssetHeadVersion, err error) {
	head.AssetID = assetID
	r := tx.Raw("SELECT asset_key,revision,COALESCE(profile_version_id,'') AS profile_version_id,COALESCE(intent_version_id,'') AS intent_version_id FROM asset_heads WHERE scope_key=? AND asset_key=?", scopeKey, assetID).Scan(&head)
	if r.Error != nil {
		return head, r.Error
	}
	return
}
func rebindAccess(access []domain.Access, scope string) []domain.Access {
	for i := range access {
		access[i].ScopeRef = scope
	}
	return access
}
func profileFromAsset(asset domain.Asset) profilePayload {
	p := profilePayload{Services: []profileService{}}
	for _, service := range asset.Services {
		p.Services = append(p.Services, profileService{service.ID, service.Role, service.RoleStatus, service.Protocol, append([]domain.PortRange(nil), service.Ports...)})
	}
	return p
}
func intentFromAsset(asset domain.Asset) intentPayload {
	p := intentPayload{AllowedConfirmed: asset.AllowedConfirmed, AllowedComplete: asset.AllowedComplete, DenyAll: asset.DenyAll, Allowed: asset.Allowed, Services: []intentService{}}
	for _, service := range asset.Services {
		p.Services = append(p.Services, intentService{service.ID, service.PublicAccess, service.AllowedConfirmed, service.AllowedComplete, service.DenyAll, service.Allowed, service.RequiredConfirmed, service.Required})
	}
	return p
}
func applyProfile(asset *domain.Asset, payload profilePayload) {
	for _, value := range payload.Services {
		service := selectedService(asset, value.ID)
		if service == nil {
			asset.Services = append(asset.Services, domain.Service{ID: value.ID, PublicAccess: "unknown", Allowed: []domain.Access{}, Required: []domain.Access{}})
			service = &asset.Services[len(asset.Services)-1]
		}
		service.Role = value.Role
		service.RoleStatus = value.RoleStatus
		service.Protocol = value.Protocol
		service.Ports = append([]domain.PortRange(nil), value.Ports...)
	}
}
func applyIntent(asset *domain.Asset, payload intentPayload, scope string) {
	asset.AllowedConfirmed = payload.AllowedConfirmed
	asset.AllowedComplete = payload.AllowedComplete
	asset.DenyAll = payload.DenyAll
	asset.Allowed = rebindAccess(payload.Allowed, scope)
	for _, value := range payload.Services {
		service := selectedService(asset, value.ID)
		if service == nil {
			// Confirmed business intent survives a later collection that has no
			// matching candidate. Its identity carries no listener or role fact.
			asset.Services = append(asset.Services, domain.Service{ID: value.ID, Role: "unknown", RoleStatus: "unknown", Protocol: "unknown", Ports: []domain.PortRange{}})
			service = &asset.Services[len(asset.Services)-1]
		}
		service.PublicAccess = value.PublicAccess
		service.AllowedConfirmed = value.AllowedConfirmed
		service.AllowedComplete = value.AllowedComplete
		service.DenyAll = value.DenyAll
		service.Allowed = rebindAccess(value.Allowed, scope)
		service.RequiredConfirmed = value.RequiredConfirmed
		service.Required = rebindAccess(value.Required, scope)
	}
}

// freezeInput reads one SQLite snapshot and projects immutable local versions
// onto a copy. Collection facts and source assurance are never upgraded here.
func freezeInput(tx *gorm.DB, snapshotID string) (in domain.Input, heads []AssetHeadVersion, err error) {
	record, err := snapshot(tx, snapshotID)
	if err != nil {
		return in, nil, err
	}
	in, err = cloneConfirmationInput(record.Input)
	if err != nil {
		return in, nil, err
	}
	key := canonicalBusinessScope(in.Scope)
	heads = []AssetHeadVersion{}
	profiles, intents := []string{}, []string{}
	for i := range in.Assets {
		asset := &in.Assets[i]
		head, e := loadAssetHead(tx, key, asset.ID)
		if e != nil {
			return in, nil, e
		}
		heads = append(heads, head)
		if head.ProfileVersionID != "" {
			var body string
			r := tx.Raw("SELECT body FROM profile_versions WHERE id=? AND scope_key=? AND asset_key=?", head.ProfileVersionID, key, asset.ID).Scan(&body)
			if r.Error != nil {
				return in, nil, r.Error
			}
			if r.RowsAffected != 1 {
				return in, nil, ErrInputInvalid
			}
			var payload profilePayload
			if e = json.Unmarshal([]byte(body), &payload); e != nil {
				return in, nil, e
			}
			applyProfile(asset, payload)
			profiles = append(profiles, asset.ID+"/"+head.ProfileVersionID)
		}
		if head.IntentVersionID != "" {
			var body string
			r := tx.Raw("SELECT body FROM intent_versions WHERE id=? AND scope_key=? AND asset_key=?", head.IntentVersionID, key, asset.ID).Scan(&body)
			if r.Error != nil {
				return in, nil, r.Error
			}
			if r.RowsAffected != 1 {
				return in, nil, ErrInputInvalid
			}
			var payload intentPayload
			if e = json.Unmarshal([]byte(body), &payload); e != nil {
				return in, nil, e
			}
			applyIntent(asset, payload, in.Scope.Ref)
			if e = replaceAssetExceptions(&in, asset.ID, payload.Exceptions, false); e != nil {
				return in, nil, e
			}
			intents = append(intents, asset.ID+"/"+head.IntentVersionID)
		}
	}
	if len(profiles) > 0 {
		sort.Strings(profiles)
		in.ProfileVersion = "profile-set_" + requestDigest(struct {
			Base     string
			Versions []string
		}{in.ProfileVersion, profiles})[:32]
	}
	if len(intents) > 0 {
		sort.Strings(intents)
		in.IntentVersion = "intent-set_" + requestDigest(struct {
			Base     string
			Versions []string
		}{in.IntentVersion, intents})[:32]
	}
	return
}
func (s *Store) FreezeInput(ctx context.Context, snapshotID string) (in domain.Input, heads []AssetHeadVersion, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var e error; in, heads, e = freezeInput(tx, snapshotID); return e })
	return
}

// checkHeadVersions is suitable for a plan-consumption transaction. Referring
// to a subset is permitted only when every referenced asset belongs to this
// immutable snapshot; the plan owns and freezes its selected subset.
func checkHeadVersions(tx *gorm.DB, snapshotID string, expected []AssetHeadVersion) error {
	record, err := snapshot(tx, snapshotID)
	if err != nil {
		return err
	}
	if len(expected) > len(record.Input.Assets) {
		return ErrInputInvalid
	}
	key := canonicalBusinessScope(record.Input.Scope)
	seen := map[string]bool{}
	for _, version := range expected {
		if version.AssetID == "" || seen[version.AssetID] || selectedAsset(&record.Input, version.AssetID) == nil || version.Revision < 0 {
			return ErrInputInvalid
		}
		seen[version.AssetID] = true
		current, err := loadAssetHead(tx, key, version.AssetID)
		if err != nil {
			return err
		}
		if current != version {
			return ErrVersionConflict
		}
	}
	return nil
}
func (s *Store) CheckHeadVersions(ctx context.Context, snapshotID string, expected []AssetHeadVersion) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return checkHeadVersions(tx, snapshotID, expected) })
}

func initialDraftChange(asset domain.Asset) AssetDraftChange {
	change := AssetDraftChange{AssetID: asset.ID, Roles: []RoleChange{}, Access: &AssetAccessChange{AllowedComplete: asset.AllowedComplete, DenyAll: asset.DenyAll, Allowed: asset.Allowed, Services: []ServiceAccessChange{}}}
	for _, service := range asset.Services {
		role := service.Role
		if role == "" {
			role = "unknown"
		}
		protocol := service.Protocol
		if protocol == "" {
			protocol = "unknown"
		}
		public := service.PublicAccess
		if public == "" {
			public = "unknown"
		}
		change.Roles = append(change.Roles, RoleChange{ServiceID: service.ID, Role: role, Protocol: protocol, Ports: service.Ports})
		change.Access.Services = append(change.Access.Services, ServiceAccessChange{ServiceID: service.ID, PublicAccess: public, AllowedComplete: service.AllowedComplete, DenyAll: service.DenyAll, Allowed: service.Allowed, RequiredConfirmed: service.RequiredConfirmed, Required: service.Required})
	}
	return change
}

func getProfileDraft(tx *gorm.DB, id string) (draft ProfileDraft, err error) {
	r := tx.Raw("SELECT id,snapshot_id,scope_key,policy_scope_ref,revision,status,body,base_json,COALESCE(preview_id,'') AS preview_id,COALESCE(confirmation_id,'') AS confirmation_id,created_at,updated_at FROM profile_drafts WHERE id=?", id).Scan(&draft)
	if r.Error != nil {
		return draft, r.Error
	}
	if r.RowsAffected == 0 {
		return draft, ErrNotFound
	}
	if err = json.Unmarshal([]byte(draft.Body), &draft.Changes); err != nil {
		return
	}
	err = json.Unmarshal([]byte(draft.BaseJSON), &draft.BaseRevisions)
	return
}
func (s *Store) GetProfileDraft(ctx context.Context, id string) (ProfileDraft, error) {
	return getProfileDraft(s.db.WithContext(ctx), id)
}
func (s *Store) CreateProfileDraft(ctx context.Context, request CreateDraftRequest) (draft ProfileDraft, err error) {
	if request.SnapshotID == "" || len(request.AssetIDs) == 0 {
		return draft, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, heads, e := freezeInput(tx, request.SnapshotID)
		if e != nil {
			return e
		}
		if len(request.AssetIDs) > len(in.Assets) {
			return ErrInputInvalid
		}
		seen := map[string]bool{}
		changes := []AssetDraftChange{}
		bases := []AssetHeadVersion{}
		for _, id := range request.AssetIDs {
			if seen[id] {
				return ErrInputInvalid
			}
			seen[id] = true
			asset := selectedAsset(&in, id)
			if asset == nil {
				return ErrInputInvalid
			}
			change := initialDraftChange(*asset)
			change.Access.Exceptions = exceptionsForAsset(in, asset.ID)
			for i := range change.Access.Exceptions {
				change.Access.Exceptions[i].Confirmed = false
			}
			changes = append(changes, change)
			for _, head := range heads {
				if head.AssetID == id {
					bases = append(bases, head)
					break
				}
			}
		}
		body, e := marshalBounded(changes, 1<<20)
		if e != nil {
			return e
		}
		baseJSON, e := marshalBounded(bases, 1<<20)
		if e != nil {
			return e
		}
		id, e := opaqueID("draft")
		if e != nil {
			return e
		}
		at := now()
		draft = ProfileDraft{ID: id, SnapshotID: request.SnapshotID, ScopeKey: canonicalBusinessScope(in.Scope), PolicyScopeRef: in.Scope.Ref, Revision: 1, Status: "draft", Changes: changes, BaseRevisions: bases, Body: string(body), BaseJSON: string(baseJSON), CreatedAt: at, UpdatedAt: at}
		return tx.Exec("INSERT INTO profile_drafts(id,snapshot_id,scope_key,policy_scope_ref,revision,status,body,base_json,created_at,updated_at) VALUES(?,?,?,?,1,'draft',?,?,?,?)", id, request.SnapshotID, draft.ScopeKey, draft.PolicyScopeRef, draft.Body, draft.BaseJSON, at, at).Error
	})
	return
}

func normalizeDraftChanges(in domain.Input, draft ProfileDraft, changes []AssetDraftChange) ([]AssetDraftChange, error) {
	if len(changes) != len(draft.BaseRevisions) || len(changes) == 0 {
		return nil, ErrInputInvalid
	}
	encoded, err := marshalBounded(changes, 1<<20)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(encoded, &changes); err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	knownNew := map[string]map[string]bool{}
	for _, head := range draft.BaseRevisions {
		targets[head.AssetID] = true
	}
	for _, old := range draft.Changes {
		knownNew[old.AssetID] = map[string]bool{}
		for _, role := range old.Roles {
			knownNew[old.AssetID][role.ServiceID] = true
		}
	}
	seen := map[string]bool{}
	for i := range changes {
		change := &changes[i]
		asset := selectedAsset(&in, change.AssetID)
		if !targets[change.AssetID] || seen[change.AssetID] || asset == nil || len(change.Roles) > 100 {
			return nil, ErrInputInvalid
		}
		seen[change.AssetID] = true
		serviceIDs := map[string]bool{}
		for _, service := range asset.Services {
			serviceIDs[service.ID] = true
		}
		roles := map[string]bool{}
		for j := range change.Roles {
			role := &change.Roles[j]
			if role.ServiceID == "" {
				role.ServiceID, err = opaqueID("service")
				if err != nil {
					return nil, err
				}
			} else if !serviceIDs[role.ServiceID] && !knownNew[change.AssetID][role.ServiceID] {
				return nil, ErrInputInvalid
			}
			if roles[role.ServiceID] || strings.TrimSpace(role.Role) == "" || len(role.Role) > 80 || !utf8.ValidString(role.Role) || !enum(role.Protocol, "tcp", "udp", "unknown") || !draftPorts(role.Ports) {
				return nil, ErrInputInvalid
			}
			roles[role.ServiceID] = true
			serviceIDs[role.ServiceID] = true
		}
		if change.Access != nil {
			if len(change.Access.Services) > 100 {
				return nil, ErrInputInvalid
			}
			if err = normalizeDraftAccess(change.Access.Allowed, in.Scope.Ref); err != nil {
				return nil, err
			}
			seenServices := map[string]bool{}
			for j := range change.Access.Services {
				access := &change.Access.Services[j]
				if !serviceIDs[access.ServiceID] || seenServices[access.ServiceID] || !enum(access.PublicAccess, "unknown", "required", "not_required") {
					return nil, ErrInputInvalid
				}
				seenServices[access.ServiceID] = true
				if err = normalizeDraftAccess(access.Allowed, in.Scope.Ref); err != nil {
					return nil, err
				}
				if err = normalizeDraftAccess(access.Required, in.Scope.Ref); err != nil {
					return nil, err
				}
			}
			if err = normalizeCandidateExceptions(change.Access.Exceptions, in, change.AssetID, serviceIDs); err != nil {
				return nil, err
			}
		}
	}
	return changes, nil
}
func draftPorts(ports []domain.PortRange) bool {
	if len(ports) > 1000 {
		return false
	}
	for _, p := range ports {
		if p.From < 1 || p.To < p.From || p.To > 65535 {
			return false
		}
	}
	return true
}
func normalizeDraftAccess(access []domain.Access, scope string) error {
	if len(access) > 1000 {
		return ErrInputInvalid
	}
	members := 0
	for i := range access {
		a := &access[i]
		if a.ScopeRef == "" {
			a.ScopeRef = scope
		}
		if a.ScopeRef != scope || !enum(a.Protocol, "tcp", "udp", "any") || !enum(a.Applicability, "applicable", "not_applicable", "unknown") || !draftPorts(a.Ports) {
			return ErrInputInvalid
		}
		members += len(a.Addresses) + len(a.Ports)
		if members > 20000 {
			return ErrInputInvalid
		}
		for _, address := range a.Addresses {
			if len(address) > 64 {
				return ErrInputInvalid
			}
		}
	}
	return nil
}
func (s *Store) UpdateProfileDraft(ctx context.Context, id string, request UpdateDraftRequest) (draft ProfileDraft, err error) {
	if request.ExpectedDraftRevision < 1 {
		return draft, ErrInputInvalid
	}
	old, err := s.GetProfileDraft(ctx, id)
	if err != nil {
		return draft, err
	}
	if old.Revision != request.ExpectedDraftRevision || !enum(old.Status, "draft", "previewed") {
		return draft, ErrVersionConflict
	}
	in, _, err := s.FreezeInput(ctx, old.SnapshotID)
	if err != nil {
		return draft, err
	}
	changes, err := normalizeDraftChanges(in, old, request.Changes)
	if err != nil {
		return draft, err
	}
	body, err := marshalBounded(changes, 1<<20)
	if err != nil {
		return draft, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, e := getProfileDraft(tx, id)
		if e != nil {
			return e
		}
		if current.Revision != request.ExpectedDraftRevision || !enum(current.Status, "draft", "previewed") {
			return ErrVersionConflict
		}
		at := now()
		r := tx.Exec("UPDATE profile_drafts SET body=?,revision=revision+1,status='draft',preview_id=NULL,updated_at=? WHERE id=? AND revision=?", string(body), at, id, request.ExpectedDraftRevision)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrVersionConflict
		}
		draft, e = getProfileDraft(tx, id)
		return e
	})
	return
}

func applyDraftSections(input domain.Input, changes []AssetDraftChange, sections []string) (domain.Input, error) {
	in, err := cloneConfirmationInput(input)
	if err != nil {
		return in, err
	}
	roles, access := false, false
	for _, section := range sections {
		switch section {
		case "roles":
			roles = true
		case "access":
			access = true
		default:
			return in, ErrInputInvalid
		}
	}
	for _, change := range changes {
		asset := selectedAsset(&in, change.AssetID)
		if asset == nil {
			return in, ErrInputInvalid
		}
		if roles {
			for _, role := range change.Roles {
				if role.Declared != nil && !*role.Declared {
					continue
				}
				service := selectedService(asset, role.ServiceID)
				if service == nil {
					asset.Services = append(asset.Services, domain.Service{ID: role.ServiceID, PublicAccess: "unknown", Allowed: []domain.Access{}, Required: []domain.Access{}})
					service = &asset.Services[len(asset.Services)-1]
				}
				service.Role = role.Role
				service.RoleStatus = "confirmed"
				if role.Role == "unknown" {
					service.RoleStatus = "unknown"
				}
				service.Protocol = role.Protocol
				service.Ports = append([]domain.PortRange(nil), role.Ports...)
			}
		}
		if access && change.Access != nil {
			value := change.Access
			if value.AllowedDeclared == nil || *value.AllowedDeclared {
				asset.AllowedConfirmed = true
				asset.AllowedComplete = value.AllowedComplete
				asset.DenyAll = value.DenyAll
				asset.Allowed = rebindAccess(value.Allowed, in.Scope.Ref)
			}
			for _, item := range value.Services {
				if item.AllowedDeclared != nil && !*item.AllowedDeclared && item.RequiredDeclared != nil && !*item.RequiredDeclared {
					continue
				}
				service := selectedService(asset, item.ServiceID)
				if service == nil {
					return in, ErrInputInvalid
				}
				if item.AllowedDeclared == nil || *item.AllowedDeclared {
					service.PublicAccess = item.PublicAccess
					service.AllowedConfirmed = true
					service.AllowedComplete = item.AllowedComplete
					service.DenyAll = item.DenyAll
					service.Allowed = rebindAccess(item.Allowed, in.Scope.Ref)
				}
				if item.RequiredDeclared == nil || *item.RequiredDeclared {
					service.RequiredConfirmed = item.RequiredConfirmed
					service.Required = rebindAccess(item.Required, in.Scope.Ref)
				}
			}
			if value.ExceptionsDeclared == nil || *value.ExceptionsDeclared {
				if err = replaceAssetExceptions(&in, asset.ID, value.Exceptions, true); err != nil {
					return in, err
				}
			}
		}
	}
	return in, nil
}

func sectionAvailable(changes []AssetDraftChange, section string) bool {
	for _, change := range changes {
		if section == "roles" {
			for _, role := range change.Roles {
				if role.Declared == nil || *role.Declared {
					return true
				}
			}
		}
		if section == "access" && change.Access != nil {
			value := change.Access
			if value.AllowedDeclared == nil || *value.AllowedDeclared || value.ExceptionsDeclared == nil || *value.ExceptionsDeclared {
				return true
			}
			for _, service := range value.Services {
				if service.AllowedDeclared == nil || *service.AllowedDeclared || service.RequiredDeclared == nil || *service.RequiredDeclared {
					return true
				}
			}
		}
	}
	return false
}
func previewForInput(ctx context.Context, draft ProfileDraft, input domain.Input) (preview ConfirmationPreview, err error) {
	preview = ConfirmationPreview{DraftID: draft.ID, DraftRevision: draft.Revision, BaseRevisions: append([]AssetHeadVersion(nil), draft.BaseRevisions...), Diff: []ConfirmationDiff{}, ConfirmableSections: []string{}, SectionDependencies: map[string][]string{}, ValidationErrors: []DraftValidationError{}, CreatedAt: now()}
	for _, section := range []string{"roles", "access"} {
		if !sectionAvailable(draft.Changes, section) {
			continue
		}
		candidate, e := applyDraftSections(input, draft.Changes, []string{section})
		if e == nil {
			e = validateProposedExceptions(ctx, input, candidate, draft.Changes, []string{section})
		}
		if e == nil {
			e = audit.ValidateContext(ctx, candidate)
		}
		if e != nil && section == "access" && sectionAvailable(draft.Changes, "roles") {
			combined, combinedErr := applyDraftSections(input, draft.Changes, []string{"roles", "access"})
			if combinedErr == nil {
				combinedErr = validateProposedExceptions(ctx, input, combined, draft.Changes, []string{"roles", "access"})
			}
			if combinedErr == nil {
				combinedErr = audit.ValidateContext(ctx, combined)
			}
			if combinedErr == nil {
				candidate = combined
				e = nil
				preview.SectionDependencies["access"] = []string{"roles"}
			}
		}
		if e != nil {
			if ctx.Err() != nil {
				return preview, ctx.Err()
			}
			for _, change := range draft.Changes {
				if sectionAvailable([]AssetDraftChange{change}, section) {
					preview.ValidationErrors = append(preview.ValidationErrors, DraftValidationError{change.AssetID, section, "BUSINESS_INPUT_INVALID"})
				}
			}
			continue
		}
		preview.ConfirmableSections = append(preview.ConfirmableSections, section)
		for _, change := range draft.Changes {
			if !sectionAvailable([]AssetDraftChange{change}, section) {
				continue
			}
			before, after := selectedAsset(&input, change.AssetID), selectedAsset(&candidate, change.AssetID)
			var a, b []byte
			if section == "roles" {
				a, _ = json.Marshal(profileFromAsset(*before))
				b, _ = json.Marshal(profileFromAsset(*after))
			} else {
				a, _ = json.Marshal(intentFromInputAsset(input, *before))
				b, _ = json.Marshal(intentFromInputAsset(candidate, *after))
			}
			preview.Diff = append(preview.Diff, ConfirmationDiff{change.AssetID, section, a, b})
		}
	}
	return preview, nil
}

func (s *Store) PreviewProfileDraft(ctx context.Context, id string) (preview ConfirmationPreview, err error) {
	if err = validateConversationDraftBinding(s.db.WithContext(ctx), id); err != nil {
		return preview, err
	}
	draft, err := s.GetProfileDraft(ctx, id)
	if err != nil {
		return preview, err
	}
	if !enum(draft.Status, "draft", "previewed") {
		return preview, ErrVersionConflict
	}
	if err = s.CheckHeadVersions(ctx, draft.SnapshotID, draft.BaseRevisions); err != nil {
		return preview, err
	}
	in, _, err := s.FreezeInput(ctx, draft.SnapshotID)
	if err != nil {
		return preview, err
	}
	preview, err = previewForInput(ctx, draft, in)
	if err != nil {
		return preview, err
	}
	preview.ID, err = opaqueID("preview")
	if err != nil {
		return preview, err
	}
	body, err := marshalBounded(preview, 2<<20)
	if err != nil {
		return preview, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, e := getProfileDraft(tx, id)
		if e != nil {
			return e
		}
		if current.Revision != draft.Revision || !enum(current.Status, "draft", "previewed") {
			return ErrVersionConflict
		}
		if e = checkHeadVersions(tx, draft.SnapshotID, draft.BaseRevisions); e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO confirmation_previews(id,draft_id,draft_revision,body,created_at) VALUES(?,?,?,?,?)", preview.ID, id, draft.Revision, string(body), preview.CreatedAt).Error; e != nil {
			return e
		}
		return tx.Exec("UPDATE profile_drafts SET status='previewed',preview_id=?,updated_at=? WHERE id=? AND revision=?", preview.ID, preview.CreatedAt, id, draft.Revision).Error
	})
	return
}

func confirmationReplay(tx *gorm.DB, key, digest string) (result ConfirmationResult, found bool, err error) {
	var row struct {
		RequestDigest string
		ResultJSON    string
	}
	r := tx.Raw("SELECT request_digest,result_json FROM confirmation_actions WHERE request_key=?", key).Scan(&row)
	if r.Error != nil {
		return result, false, r.Error
	}
	if r.RowsAffected == 0 {
		return result, false, nil
	}
	if row.RequestDigest != digest {
		return result, false, ErrConflict
	}
	err = json.Unmarshal([]byte(row.ResultJSON), &result)
	return result, true, err
}
func (s *Store) findConfirmationReplay(ctx context.Context, key, digest string) (result ConfirmationResult, found bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var e error; result, found, e = confirmationReplay(tx, key, digest); return e })
	return
}

func (s *Store) ConfirmProfileDraft(ctx context.Context, id string, request ConfirmDraftRequest, key string) (result ConfirmationResult, err error) {
	if !validKey(key) || request.PreviewID == "" || request.ExpectedDraftRevision < 1 || len(request.ConfirmedSections) == 0 || len(request.ConfirmedSections) > 2 {
		return result, ErrInputInvalid
	}
	sections := append([]string(nil), request.ConfirmedSections...)
	sort.Strings(sections)
	for i, section := range sections {
		if !enum(section, "roles", "access") || (i > 0 && sections[i-1] == section) {
			return result, ErrInputInvalid
		}
	}
	request.ConfirmedSections = sections
	digest := requestDigest(struct {
		Kind, Draft string
		Request     ConfirmDraftRequest
	}{"confirm", id, request})
	if replay, found, e := s.findConfirmationReplay(ctx, key, digest); e != nil || found {
		return replay, e
	}
	if err = validateConversationDraftBinding(s.db.WithContext(ctx), id); err != nil {
		return result, err
	}
	draft, err := s.GetProfileDraft(ctx, id)
	if err != nil {
		return result, err
	}
	if draft.Revision != request.ExpectedDraftRevision || draft.Status != "previewed" || draft.PreviewID != request.PreviewID {
		return result, ErrVersionConflict
	}
	in, _, err := s.FreezeInput(ctx, draft.SnapshotID)
	if err != nil {
		return result, err
	}
	if err = s.CheckHeadVersions(ctx, draft.SnapshotID, draft.BaseRevisions); err != nil {
		return result, err
	}
	var preview ConfirmationPreview
	var previewBody string
	r := s.db.WithContext(ctx).Raw("SELECT body FROM confirmation_previews WHERE id=? AND draft_id=? AND draft_revision=?", request.PreviewID, id, draft.Revision).Scan(&previewBody)
	if r.Error != nil {
		return result, r.Error
	}
	if r.RowsAffected != 1 || json.Unmarshal([]byte(previewBody), &preview) != nil {
		return result, ErrVersionConflict
	}
	for _, section := range sections {
		if !containsSection(preview.ConfirmableSections, section) {
			return result, ErrInputInvalid
		}
		for _, required := range preview.SectionDependencies[section] {
			if !containsSection(sections, required) {
				return result, ErrInputInvalid
			}
		}
	}
	candidate, err := applyDraftSections(in, draft.Changes, sections)
	if err != nil {
		return result, err
	}
	if err = validateProposedExceptions(ctx, in, candidate, draft.Changes, sections); err != nil {
		return result, ErrInputInvalid
	}
	if err = audit.ValidateContext(ctx, candidate); err != nil {
		return result, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, e := confirmationReplay(tx, key, digest); e != nil || found {
			result = replay
			return e
		}
		if e := validateConversationDraftBinding(tx, id); e != nil {
			return e
		}
		current, e := getProfileDraft(tx, id)
		if e != nil {
			return e
		}
		if current.Revision != request.ExpectedDraftRevision || current.Status != "previewed" || current.PreviewID != request.PreviewID {
			return ErrVersionConflict
		}
		if e = checkHeadVersions(tx, draft.SnapshotID, draft.BaseRevisions); e != nil {
			return e
		}
		if e = validateProposedExceptions(ctx, in, candidate, draft.Changes, sections); e != nil {
			return ErrInputInvalid
		}
		heads := []AssetHeadVersion{}
		at := now()
		for _, change := range draft.Changes {
			head, e := loadAssetHead(tx, draft.ScopeKey, change.AssetID)
			if e != nil {
				return e
			}
			before := head
			asset := selectedAsset(&candidate, change.AssetID)
			if asset == nil {
				return ErrInputInvalid
			}
			if containsSection(sections, "roles") && sectionAvailable([]AssetDraftChange{change}, "roles") {
				version, e := opaqueID("profile")
				if e != nil {
					return e
				}
				payload, e := marshalBounded(profileFromAsset(*asset), 1<<20)
				if e != nil {
					return e
				}
				if e = tx.Exec("INSERT INTO profile_versions(id,scope_key,asset_key,body,source_snapshot_id,confirmed_at) VALUES(?,?,?,?,?,?)", version, draft.ScopeKey, change.AssetID, string(payload), draft.SnapshotID, at).Error; e != nil {
					return e
				}
				head.ProfileVersionID = version
			}
			if containsSection(sections, "access") && sectionAvailable([]AssetDraftChange{change}, "access") {
				version, e := opaqueID("intent")
				if e != nil {
					return e
				}
				payload, e := marshalBounded(intentFromInputAsset(candidate, *asset), 1<<20)
				if e != nil {
					return e
				}
				if e = tx.Exec("INSERT INTO intent_versions(id,scope_key,asset_key,body,source_snapshot_id,confirmed_at) VALUES(?,?,?,?,?,?)", version, draft.ScopeKey, change.AssetID, string(payload), draft.SnapshotID, at).Error; e != nil {
					return e
				}
				head.IntentVersionID = version
			}
			if head.ProfileVersionID != before.ProfileVersionID || head.IntentVersionID != before.IntentVersionID {
				if e = tx.Exec("INSERT OR IGNORE INTO asset_heads(scope_key,asset_key,revision) VALUES(?,?,0)", draft.ScopeKey, change.AssetID).Error; e != nil {
					return e
				}
				head.Revision++
				r := tx.Exec("UPDATE asset_heads SET revision=?,profile_version_id=?,intent_version_id=? WHERE scope_key=? AND asset_key=? AND revision=?", head.Revision, nullableConfirmationID(head.ProfileVersionID), nullableConfirmationID(head.IntentVersionID), draft.ScopeKey, change.AssetID, before.Revision)
				if r.Error != nil {
					return r.Error
				}
				if r.RowsAffected != 1 {
					return ErrVersionConflict
				}
			}
			heads = append(heads, head)
		}
		actionID, e := opaqueID("confirmation")
		if e != nil {
			return e
		}
		result = ConfirmationResult{ActionID: actionID, DraftID: id, Status: "confirmed", Revision: draft.Revision + 1, Heads: heads, SavedAt: at}
		body, e := marshalBounded(result, 1<<20)
		if e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO confirmation_actions(id,draft_id,kind,request_key,request_digest,result_json,saved_at) VALUES(?,?,'confirm',?,?,?,?)", actionID, id, key, digest, string(body), at).Error; e != nil {
			return e
		}
		if advanced, e := advanceConversationDraftAfterConfirmation(tx, draft, sections, heads, actionID, at); e != nil || advanced {
			return e
		}
		r := tx.Exec("UPDATE profile_drafts SET status='confirmed',revision=revision+1,confirmation_id=?,updated_at=? WHERE id=? AND revision=?", actionID, at, id, draft.Revision)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrVersionConflict
		}
		return nil
	})
	return
}
func containsSection(sections []string, value string) bool {
	for _, section := range sections {
		if section == value {
			return true
		}
	}
	return false
}
func nullableConfirmationID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func (s *Store) RejectProfileDraft(ctx context.Context, id string, expectedRevision int64, key string) (result ConfirmationResult, err error) {
	if !validKey(key) || expectedRevision < 1 {
		return result, ErrInputInvalid
	}
	digest := requestDigest(struct {
		Kind, Draft string
		Revision    int64
	}{"reject", id, expectedRevision})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, e := confirmationReplay(tx, key, digest); e != nil || found {
			result = replay
			return e
		}
		draft, e := getProfileDraft(tx, id)
		if e != nil {
			return e
		}
		if draft.Revision != expectedRevision || !enum(draft.Status, "draft", "previewed") {
			return ErrVersionConflict
		}
		heads := []AssetHeadVersion{}
		for _, base := range draft.BaseRevisions {
			head, e := loadAssetHead(tx, draft.ScopeKey, base.AssetID)
			if e != nil {
				return e
			}
			heads = append(heads, head)
		}
		actionID, e := opaqueID("confirmation")
		if e != nil {
			return e
		}
		at := now()
		result = ConfirmationResult{ActionID: actionID, DraftID: id, Status: "rejected", Revision: draft.Revision + 1, Heads: heads, SavedAt: at}
		body, e := marshalBounded(result, 1<<20)
		if e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO confirmation_actions(id,draft_id,kind,request_key,request_digest,result_json,saved_at) VALUES(?,?,'reject',?,?,?,?)", actionID, id, key, digest, string(body), at).Error; e != nil {
			return e
		}
		r := tx.Exec("UPDATE profile_drafts SET status='rejected',revision=revision+1,preview_id=NULL,confirmation_id=?,updated_at=? WHERE id=? AND revision=?", actionID, at, id, expectedRevision)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrVersionConflict
		}
		return nil
	})
	return
}
