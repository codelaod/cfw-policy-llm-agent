package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/sensitive"
	"gorm.io/gorm"
)

// The bound is on one local confirmation, not the size of the cloud catalogue.
const ResourceGroupMemberLimit = 1000

var ErrResourceGroupTarget = errors.New("resource group target unresolved")
var ErrResourceGroupAmbiguous = errors.New("resource group source/account ambiguous")

type ResourceGroupMember struct {
	ResourceRef     string `json:"resource_ref"`
	IdentityRef     string `json:"identity_ref"`
	Provider        string `json:"provider"`
	SourceRef       string `json:"source_ref"`
	AccountRef      string `json:"account_ref"`
	Region          string `json:"region"`
	ResourceType    string `json:"resource_type"`
	ResourceID      string `json:"resource_id,omitempty"`
	LocalAssetID    string `json:"local_asset_id,omitempty"`
	IdentityContext string `json:"identity_context,omitempty"`
	Name            string `json:"name"`
	Source          string `json:"source"`
	ObservedAt      string `json:"observed_at"`
}
type ResourceGroupTarget struct {
	ConversationID string   `json:"conversation_id"`
	IntentID       string   `json:"intent_id"`
	IntentRevision int64    `json:"intent_revision"`
	ResourceRefs   []string `json:"resource_refs"`
	Digest         string   `json:"-"`
}
type ResourceGroupPreview struct {
	ID                  string                `json:"id"`
	GroupID             string                `json:"group_id"`
	GroupName           string                `json:"group_name"`
	SourceRef           string                `json:"source_ref"`
	AccountRef          string                `json:"account_ref"`
	ConversationID      string                `json:"conversation_id"`
	IntentID            string                `json:"intent_id"`
	IntentRevision      int64                 `json:"intent_revision"`
	BaseRevision        int64                 `json:"base_revision"`
	AuthorizationSource string                `json:"authorization_source"`
	Members             []ResourceGroupMember `json:"members"`
	CreatedAt           string                `json:"created_at"`
	ExpiresAt           string                `json:"expires_at"`
	TargetDigest        string                `json:"-"`
}
type ResourceGroupVersion struct {
	ID                  string                `json:"id"`
	GroupID             string                `json:"group_id"`
	PreviewID           string                `json:"preview_id"`
	Revision            int64                 `json:"revision"`
	GroupName           string                `json:"group_name"`
	SourceRef           string                `json:"source_ref"`
	AccountRef          string                `json:"account_ref"`
	ConversationID      string                `json:"conversation_id"`
	IntentID            string                `json:"intent_id"`
	IntentRevision      int64                 `json:"intent_revision"`
	AuthorizationSource string                `json:"authorization_source"`
	Members             []ResourceGroupMember `json:"members"`
	CreatedAt           string                `json:"created_at"`
	CatalogueRevision   int64                 `json:"catalogue_revision"`
}
type ResourceGroupSummary struct {
	GroupID     string `json:"group_id"`
	VersionID   string `json:"version_id"`
	Name        string `json:"name"`
	Revision    int64  `json:"revision"`
	SourceRef   string `json:"source_ref"`
	AccountRef  string `json:"account_ref"`
	ConfirmedAt string `json:"confirmed_at"`
}
type ResourceGroupRequest struct {
	ClientDigest        string   `json:"-"`
	Action              string   `json:"action"`
	GroupName           string   `json:"group_name,omitempty"`
	ResourceRefs        []string `json:"resource_refs,omitempty"`
	AuthorizationSource string   `json:"authorization_source,omitempty"`
	ExpectedRevision    int64    `json:"expected_revision"`
	PreviewID           string   `json:"preview_id,omitempty"`
}
type ResourceGroupResult struct {
	State    string                `json:"state"`
	Preview  *ResourceGroupPreview `json:"preview,omitempty"`
	Version  *ResourceGroupVersion `json:"version,omitempty"`
	Missing  []string              `json:"missing"`
	Replayed bool                  `json:"replayed"`
}

func migrateResourceGroupSchema(tx *gorm.DB) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS resource_group_versions(id TEXT PRIMARY KEY,group_id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),preview_id TEXT UNIQUE NOT NULL,source_ref TEXT NOT NULL,account_ref TEXT NOT NULL,name TEXT NOT NULL,created_at TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),UNIQUE(group_id,revision))`,
		`CREATE TABLE IF NOT EXISTS resource_group_heads(group_id TEXT PRIMARY KEY,source_ref TEXT NOT NULL,account_ref TEXT NOT NULL,name TEXT NOT NULL,revision INTEGER NOT NULL,version_id TEXT NOT NULL REFERENCES resource_group_versions(id),UNIQUE(source_ref,account_ref,name),FOREIGN KEY(group_id,revision) REFERENCES resource_group_versions(group_id,revision))`,
		`CREATE TABLE IF NOT EXISTS resource_group_members(version_id TEXT NOT NULL REFERENCES resource_group_versions(id),resource_ref TEXT NOT NULL REFERENCES resource_catalog(id),PRIMARY KEY(version_id,resource_ref))`,
		`CREATE INDEX IF NOT EXISTS resource_group_member_lookup ON resource_group_members(resource_ref,version_id)`,
		`CREATE TABLE IF NOT EXISTS resource_group_previews(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),intent_id TEXT NOT NULL,intent_revision INTEGER NOT NULL,target_digest TEXT NOT NULL,created_at TEXT NOT NULL,expires_at TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)))`,
		`CREATE TABLE IF NOT EXISTS resource_group_requests(request_key TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),request_digest TEXT NOT NULL,result_json TEXT NOT NULL CHECK(json_valid(result_json)))`,
	} {
		if e := tx.Exec(statement).Error; e != nil {
			return e
		}
	}
	return checkResourceGroupSchema(tx)
}
func checkResourceGroupSchema(db *gorm.DB) error {
	for table, columns := range map[string][]string{
		"resource_group_versions": {"id", "group_id", "revision", "preview_id", "source_ref", "account_ref", "name", "created_at", "body"},
		"resource_group_heads":    {"group_id", "source_ref", "account_ref", "name", "revision", "version_id"},
		"resource_group_members":  {"version_id", "resource_ref"},
		"resource_group_previews": {"id", "conversation_id", "intent_id", "intent_revision", "target_digest", "created_at", "expires_at", "body"},
		"resource_group_requests": {"request_key", "conversation_id", "request_digest", "result_json"},
	} {
		var names []struct{ Name string }
		if e := db.Raw("SELECT name FROM pragma_table_info(?)", table).Scan(&names).Error; e != nil {
			return e
		}
		got := map[string]bool{}
		for _, name := range names {
			got[name.Name] = true
		}
		for _, column := range columns {
			if !got[column] {
				return errors.New("resource group schema incomplete; explicit migration required")
			}
		}
	}
	var inconsistent int64
	if e := db.Raw(`SELECT count(*) FROM resource_group_versions v WHERE COALESCE(json_type(v.body,'$.members'),'') <> 'array' OR (SELECT count(*) FROM resource_group_members m WHERE m.version_id=v.id) <> json_array_length(v.body,'$.members')`).Scan(&inconsistent).Error; e != nil {
		return e
	}
	if inconsistent != 0 {
		return errors.New("resource group membership inconsistent; explicit recovery required")
	}
	if e := db.Raw(`SELECT count(*) FROM resource_group_heads h LEFT JOIN resource_group_versions v ON v.id=h.version_id WHERE v.id IS NULL OR h.group_id<>v.group_id OR h.revision<>v.revision OR h.source_ref<>v.source_ref OR h.account_ref<>v.account_ref OR h.name<>v.name`).Scan(&inconsistent).Error; e != nil {
		return e
	}
	if inconsistent != 0 {
		return errors.New("resource group head inconsistent; explicit recovery required")
	}
	return nil
}

// ResourceGroupAuthorityDeclared rejects explicit placeholders only. A named
// document/title may contain these words; this is a user's attestation, not an
// independent verification of a ticket or business approval.
func ResourceGroupAuthorityDeclared(value string) bool {
	normalized := strings.ToLower(strings.Trim(value, " \t\r\n，。；:：!?！？()（）[]【】\"“”"))
	for _, prefix := range []string{"授权来源", "授权依据", "来源"} {
		if strings.HasPrefix(normalized, prefix) {
			normalized = strings.Trim(strings.TrimPrefix(normalized, prefix), " \t，。；:：=\"“”")
			break
		}
	}
	switch normalized {
	case "", "未知", "不知道", "不清楚", "不确定", "尚不清楚", "暂不清楚", "暂不确定", "未核实", "尚未核实", "未经核实", "未确认", "尚未确认", "待确认", "待核实", "待补充", "未提供", "尚未提供", "暂无", "无", "没有", "unknown", "unknown source", "unsure", "not sure", "unverified", "not verified", "not confirmed", "none", "null", "n/a", "na":
		return false
	}
	return true
}
func groupText(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	safe, _ := sensitive.Redact(value, nil)
	return safe == value
}
func groupMember(resource domain.Resource) ResourceGroupMember {
	return ResourceGroupMember{ResourceRef: resource.ID, IdentityRef: resource.IdentityRef, Provider: resource.Provider, SourceRef: resource.SourceRef, AccountRef: resource.AccountRef, Region: resource.Region, ResourceType: resource.ResourceType, ResourceID: resource.ResourceID, LocalAssetID: resource.LocalAssetID, IdentityContext: resource.IdentityContext, Name: resource.Name, Source: resource.Source, ObservedAt: resource.ObservedAt}
}
func groupMemberIdentity(member ResourceGroupMember) ResourceGroupMember {
	member.Name = ""
	member.Source = ""
	member.ObservedAt = ""
	if member.ResourceID != "" {
		member.LocalAssetID = ""
		member.IdentityContext = ""
	}
	return member
}
func groupDigest(conversation string, req ResourceGroupRequest) string {
	if req.ClientDigest != "" {
		return requestDigest([]string{"resource-group-v1", conversation, req.ClientDigest})
	}
	req.ClientDigest = ""
	return requestDigest([]any{"resource-group-v1", conversation, req})
}
func groupReplay(tx *gorm.DB, conversation, key, digest string) (result ResourceGroupResult, found bool, err error) {
	var row struct{ ConversationID, RequestDigest, ResultJSON string }
	q := tx.Raw("SELECT * FROM resource_group_requests WHERE request_key=?", key).Scan(&row)
	if q.Error != nil {
		return result, false, q.Error
	}
	if q.RowsAffected == 0 {
		return result, false, nil
	}
	if row.ConversationID != conversation || row.RequestDigest != digest {
		return result, true, ErrConflict
	}
	if json.Unmarshal([]byte(row.ResultJSON), &result) != nil {
		return result, true, ErrInputInvalid
	}
	result.Replayed = true
	return result, true, nil
}
func (s *Store) ResourceGroupReplay(ctx context.Context, conversation, key, clientDigest string) (ResourceGroupResult, bool, error) {
	if !validKey(key) || !validKey(clientDigest) {
		return ResourceGroupResult{}, false, ErrInputInvalid
	}
	return groupReplay(s.db.WithContext(ctx), conversation, key, groupDigest(conversation, ResourceGroupRequest{ClientDigest: clientDigest}))
}

func resourceGroupSelection(tx *gorm.DB, conversation string) (target ResourceGroupTarget, members []ResourceGroupMember, err error) {
	c, e := getConversation(tx, conversation)
	if e != nil {
		return target, nil, e
	}
	if c.Archived {
		return target, nil, ErrConversationArchived
	}
	intent, e := currentIntent(tx, conversation)
	if e != nil {
		if errors.Is(e, ErrNotFound) {
			e = ErrResourceGroupTarget
		}
		return target, nil, e
	}
	selected := append([]domain.Resource{}, intent.Selection...)
	if len(selected) == 0 && intent.Proposal != nil && intent.Proposal.PlanID != "" {
		p, e := planRecord(tx, intent.Proposal.PlanID)
		if e != nil {
			return target, nil, e
		}
		if p.ConversationID != conversation || p.ConsumedJobID == "" {
			return target, nil, ErrResourceGroupTarget
		}
		job, e := get(tx, p.ConsumedJobID)
		if e != nil {
			return target, nil, e
		}
		if job.ConversationID != conversation || job.PlanID != p.ID {
			return target, nil, ErrInputInvalid
		}
		var in domain.Input
		binding, e := preparedBinding(tx, job.ID)
		if e == nil {
			in = binding.Input
		} else if errors.Is(e, ErrNotFound) {
			in, e = jobInput(tx, job.ID)
		}
		if e != nil {
			return target, nil, ErrResourceGroupTarget
		}
		for _, a := range in.Assets {
			r := domain.Resource{Provider: in.Scope.Provider, AccountRef: in.Scope.AccountRef, Region: a.Region, ResourceType: a.ResourceType, ResourceID: a.ProviderResourceID, LocalAssetID: a.ID, IdentityContext: in.Scope.Ref, SourceRef: p.DataSourceID}
			r.IdentityRef = canonicalResourceID(r)
			r.ID = resourceID(r)
			selected = append(selected, r)
		}
		if in.GatewayMetadata != nil && in.Selection.Kind == "gateway_policy" {
			r := *in.GatewayMetadata
			r.SourceRef = p.DataSourceID
			r.Provider = in.Scope.Provider
			r.AccountRef = in.Scope.AccountRef
			r.IdentityRef = canonicalResourceID(r)
			r.ID = resourceID(r)
			selected = append(selected, r)
		}
	}
	if len(selected) == 0 || len(selected) > ResourceGroupMemberLimit || enum(intent.Status, "needs_clarification", "ambiguous", "clarification_required") {
		return target, nil, ErrResourceGroupTarget
	}
	seen := map[string]bool{}
	for _, chosen := range selected {
		if !validKey(chosen.ID) || seen[chosen.ID] {
			return target, nil, ErrInputInvalid
		}
		seen[chosen.ID] = true
		var row struct{ ID, SourceRef, AccountRef, Body string }
		q := tx.Raw("SELECT id,source_ref,account_ref,body FROM resource_catalog WHERE id=?", chosen.ID).Scan(&row)
		if q.Error != nil {
			return target, nil, q.Error
		}
		if q.RowsAffected != 1 {
			return target, nil, ErrResourceGroupTarget
		}
		var actual domain.Resource
		if json.Unmarshal([]byte(row.Body), &actual) != nil || actual.ID != row.ID || actual.SourceRef != row.SourceRef || actual.AccountRef != row.AccountRef || actual.IdentityRef != canonicalResourceID(actual) || actual.ID != resourceID(actual) {
			return target, nil, ErrInputInvalid
		}
		if groupMemberIdentity(groupMember(chosen)) != groupMemberIdentity(groupMember(actual)) {
			return target, nil, ErrVersionConflict
		}
		members = append(members, groupMember(actual))
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ResourceRef < members[j].ResourceRef })
	identities := make([]ResourceGroupMember, 0, len(members))
	refs := []string{}
	for _, member := range members {
		identities = append(identities, groupMemberIdentity(member))
		refs = append(refs, member.ResourceRef)
	}
	target = ResourceGroupTarget{ConversationID: conversation, IntentID: intent.ID, IntentRevision: intent.Revision, ResourceRefs: refs}
	target.Digest = requestDigest(struct {
		Conversation, Intent, Scope string
		Revision                    int64
		Members                     []ResourceGroupMember
	}{conversation, intent.ID, c.PolicyScopeRef, intent.Revision, identities})
	return target, members, nil
}
func (s *Store) ResourceGroupTarget(ctx context.Context, conversation string) (target ResourceGroupTarget, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		target, _, e = resourceGroupSelection(tx, conversation)
		return e
	})
	return
}
func getResourceGroupPreview(tx *gorm.DB, conversation, id string) (preview ResourceGroupPreview, err error) {
	var row struct{ ID, ConversationID, TargetDigest, Body string }
	q := tx.Raw("SELECT id,conversation_id,target_digest,body FROM resource_group_previews WHERE id=?", id).Scan(&row)
	if q.Error != nil {
		return preview, q.Error
	}
	if q.RowsAffected != 1 {
		return preview, ErrNotFound
	}
	if row.ConversationID != conversation {
		return preview, ErrInputInvalid
	}
	if json.Unmarshal([]byte(row.Body), &preview) != nil || preview.ID != row.ID || preview.ConversationID != conversation {
		return preview, ErrInputInvalid
	}
	preview.TargetDigest = row.TargetDigest
	return
}
func (s *Store) ResourceGroupPreview(ctx context.Context, conversation, id string) (ResourceGroupPreview, error) {
	return getResourceGroupPreview(s.db.WithContext(ctx), conversation, id)
}
func (s *Store) LatestResourceGroupPreview(ctx context.Context, conversation string) (ResourceGroupPreview, error) {
	var id string
	q := s.db.WithContext(ctx).Raw("SELECT id FROM resource_group_previews WHERE conversation_id=? ORDER BY created_at DESC,id DESC LIMIT 1", conversation).Scan(&id)
	if q.Error != nil {
		return ResourceGroupPreview{}, q.Error
	}
	if id == "" {
		return ResourceGroupPreview{}, ErrNotFound
	}
	return getResourceGroupPreview(s.db.WithContext(ctx), conversation, id)
}
func resourceGroupVersion(tx *gorm.DB, id string) (version ResourceGroupVersion, err error) {
	var row struct{ ID, Body string }
	q := tx.Raw("SELECT id,body FROM resource_group_versions WHERE id=?", id).Scan(&row)
	if q.Error != nil {
		return version, q.Error
	}
	if q.RowsAffected != 1 {
		return version, ErrNotFound
	}
	if json.Unmarshal([]byte(row.Body), &version) != nil || version.ID != row.ID {
		return version, ErrInputInvalid
	}
	return
}
func (s *Store) ResourceGroupVersion(ctx context.Context, id string) (ResourceGroupVersion, error) {
	return resourceGroupVersion(s.db.WithContext(ctx), id)
}
func resourceGroupHeads(tx *gorm.DB, name, source, account string) (heads []ResourceGroupSummary, err error) {
	q := tx.Table("resource_group_heads h").Select("h.group_id,h.version_id,h.name,h.revision,h.source_ref,h.account_ref,v.created_at AS confirmed_at").Joins("JOIN resource_group_versions v ON v.id=h.version_id")
	if name != "" {
		q = q.Where("h.name=?", name)
	}
	if source != "" {
		q = q.Where("h.source_ref=?", source)
	}
	if account != "" {
		q = q.Where("h.account_ref=?", account)
	}
	err = q.Order("h.source_ref,h.account_ref,h.name,h.group_id").Scan(&heads).Error
	return
}
func (s *Store) ResourceGroup(ctx context.Context, source, account, name string) (ResourceGroupVersion, error) {
	heads, e := resourceGroupHeads(s.db.WithContext(ctx), name, source, account)
	if e != nil {
		return ResourceGroupVersion{}, e
	}
	if len(heads) == 0 {
		return ResourceGroupVersion{}, ErrNotFound
	}
	if len(heads) != 1 {
		return ResourceGroupVersion{}, ErrResourceGroupAmbiguous
	}
	return resourceGroupVersion(s.db.WithContext(ctx), heads[0].VersionID)
}
func (s *Store) PreviewResourceGroup(ctx context.Context, conversation, groupName string, resourceRefs []string, authorizationSource string, expectedRevision int64, key string) (ResourceGroupPreview, error) {
	out, e := s.ApplyResourceGroup(ctx, conversation, key, ResourceGroupRequest{Action: "preview", GroupName: groupName, ResourceRefs: resourceRefs, AuthorizationSource: authorizationSource, ExpectedRevision: expectedRevision})
	if e != nil {
		return ResourceGroupPreview{}, e
	}
	if out.Preview == nil {
		return ResourceGroupPreview{}, ErrInputInvalid
	}
	return *out.Preview, nil
}
func (s *Store) ConfirmResourceGroup(ctx context.Context, conversation, previewID string, expectedRevision int64, key string) (ResourceGroupVersion, error) {
	out, e := s.ApplyResourceGroup(ctx, conversation, key, ResourceGroupRequest{Action: "confirm", PreviewID: previewID, ExpectedRevision: expectedRevision})
	if e != nil {
		return ResourceGroupVersion{}, e
	}
	if out.Version == nil {
		return ResourceGroupVersion{}, ErrInputInvalid
	}
	return *out.Version, nil
}
func (s *Store) ApplyResourceGroup(ctx context.Context, conversation, key string, req ResourceGroupRequest) (out ResourceGroupResult, err error) {
	if !validKey(key) || !validKey(conversation) || req.ClientDigest != "" && !validKey(req.ClientDigest) {
		return out, ErrInputInvalid
	}
	req.ResourceRefs = append([]string(nil), req.ResourceRefs...)
	digest := groupDigest(conversation, req)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var replayed bool
		var e error
		out, replayed, e = groupReplay(tx, conversation, key, digest)
		if e != nil || replayed {
			return e
		}
		if !enum(req.Action, "preview", "confirm") || req.ExpectedRevision < 1 {
			return ErrInputInvalid
		}
		target, selected, e := resourceGroupSelection(tx, conversation)
		if e != nil {
			return e
		}
		if target.IntentRevision != req.ExpectedRevision {
			return ErrVersionConflict
		}
		out = ResourceGroupResult{Missing: []string{}}
		if req.Action == "preview" {
			if !groupText(req.GroupName, 256) || (!groupText(req.AuthorizationSource, 2048) || !ResourceGroupAuthorityDeclared(req.AuthorizationSource)) || len(req.ResourceRefs) > ResourceGroupMemberLimit {
				return ErrInputInvalid
			}
			wanted := map[string]bool{}
			for _, ref := range req.ResourceRefs {
				if !validKey(ref) {
					return ErrInputInvalid
				}
				wanted[ref] = true
			}
			if len(wanted) == 0 {
				for _, ref := range target.ResourceRefs {
					wanted[ref] = true
				}
			}
			members := []ResourceGroupMember{}
			for _, member := range selected {
				if wanted[member.ResourceRef] {
					members = append(members, member)
					delete(wanted, member.ResourceRef)
				}
			}
			if len(wanted) != 0 || len(members) == 0 {
				return ErrInputInvalid
			}
			source, account := members[0].SourceRef, members[0].AccountRef
			for _, member := range members {
				if member.SourceRef != source || member.AccountRef != account || member.Provider != members[0].Provider {
					return ErrInputInvalid
				}
			}
			groupID := "resource_group_" + requestDigest([]string{source, account, req.GroupName})[:32]
			var base int64
			if e = tx.Raw("SELECT revision FROM resource_group_heads WHERE group_id=?", groupID).Scan(&base).Error; e != nil {
				return e
			}
			id, e := opaqueID("group_preview")
			if e != nil {
				return e
			}
			at := time.Now().UTC()
			preview := ResourceGroupPreview{ID: id, GroupID: groupID, GroupName: req.GroupName, SourceRef: source, AccountRef: account, ConversationID: conversation, IntentID: target.IntentID, IntentRevision: target.IntentRevision, BaseRevision: base, AuthorizationSource: req.AuthorizationSource, Members: members, CreatedAt: at.Format(time.RFC3339Nano), ExpiresAt: at.Add(5 * time.Minute).Format(time.RFC3339Nano), TargetDigest: target.Digest}
			body, e := marshalBounded(preview, 1<<20)
			if e != nil {
				return e
			}
			if e = tx.Exec("INSERT INTO resource_group_previews(id,conversation_id,intent_id,intent_revision,target_digest,created_at,expires_at,body) VALUES(?,?,?,?,?,?,?,?)", id, conversation, preview.IntentID, preview.IntentRevision, preview.TargetDigest, preview.CreatedAt, preview.ExpiresAt, string(body)).Error; e != nil {
				return e
			}
			out.State = "preview"
			out.Preview = &preview
		} else {
			if !validKey(req.PreviewID) {
				return ErrInputInvalid
			}
			preview, e := getResourceGroupPreview(tx, conversation, req.PreviewID)
			if e != nil {
				return e
			}
			if !ResourceGroupAuthorityDeclared(preview.AuthorizationSource) {
				return ErrInputInvalid
			}
			if req.GroupName != "" && req.GroupName != preview.GroupName || req.AuthorizationSource != "" && req.AuthorizationSource != preview.AuthorizationSource {
				return ErrInputInvalid
			}
			if len(req.ResourceRefs) > 0 {
				supplied := map[string]bool{}
				for _, ref := range req.ResourceRefs {
					supplied[ref] = true
				}
				if len(supplied) != len(preview.Members) {
					return ErrInputInvalid
				}
				for _, member := range preview.Members {
					if !supplied[member.ResourceRef] {
						return ErrInputInvalid
					}
				}
			}
			expiry, e := time.Parse(time.RFC3339Nano, preview.ExpiresAt)
			if e != nil || !time.Now().Before(expiry) || preview.IntentID != target.IntentID || preview.IntentRevision != target.IntentRevision || preview.TargetDigest != target.Digest {
				return ErrVersionConflict
			}
			byRef := map[string]ResourceGroupMember{}
			for _, member := range selected {
				byRef[member.ResourceRef] = member
			}
			for _, member := range preview.Members {
				actual, found := byRef[member.ResourceRef]
				if !found || groupMemberIdentity(actual) != groupMemberIdentity(member) {
					return ErrVersionConflict
				}
			}
			var revision int64
			if e = tx.Raw("SELECT revision FROM resource_group_heads WHERE group_id=?", preview.GroupID).Scan(&revision).Error; e != nil {
				return e
			}
			if revision != preview.BaseRevision {
				return ErrVersionConflict
			}
			bumped := tx.Exec("UPDATE resource_catalogue_state SET revision=revision+1 WHERE id=1")
			if bumped.Error != nil {
				return bumped.Error
			}
			if bumped.RowsAffected != 1 {
				return ErrInputInvalid
			}
			var catalogue int64
			if e = tx.Raw("SELECT revision FROM resource_catalogue_state WHERE id=1").Scan(&catalogue).Error; e != nil {
				return e
			}
			id, e := opaqueID("group_version")
			if e != nil {
				return e
			}
			version := ResourceGroupVersion{ID: id, GroupID: preview.GroupID, PreviewID: preview.ID, Revision: revision + 1, GroupName: preview.GroupName, SourceRef: preview.SourceRef, AccountRef: preview.AccountRef, ConversationID: conversation, IntentID: preview.IntentID, IntentRevision: preview.IntentRevision, AuthorizationSource: preview.AuthorizationSource, Members: preview.Members, CreatedAt: now(), CatalogueRevision: catalogue}
			body, e := marshalBounded(version, 1<<20)
			if e != nil {
				return e
			}
			if e = tx.Exec("INSERT INTO resource_group_versions(id,group_id,revision,preview_id,source_ref,account_ref,name,created_at,body) VALUES(?,?,?,?,?,?,?,?,?)", id, version.GroupID, version.Revision, preview.ID, version.SourceRef, version.AccountRef, version.GroupName, version.CreatedAt, string(body)).Error; e != nil {
				return e
			}
			for _, member := range version.Members {
				if e = tx.Exec("INSERT INTO resource_group_members(version_id,resource_ref) VALUES(?,?)", id, member.ResourceRef).Error; e != nil {
					return e
				}
			}
			var head *gorm.DB
			if revision == 0 {
				head = tx.Exec("INSERT INTO resource_group_heads(group_id,source_ref,account_ref,name,revision,version_id) VALUES(?,?,?,?,?,?) ON CONFLICT(group_id) DO NOTHING", version.GroupID, version.SourceRef, version.AccountRef, version.GroupName, version.Revision, id)
			} else {
				head = tx.Exec("UPDATE resource_group_heads SET revision=?,version_id=? WHERE group_id=? AND revision=?", version.Revision, id, version.GroupID, revision)
			}
			if head.Error != nil {
				return head.Error
			}
			if head.RowsAffected != 1 {
				return ErrVersionConflict
			}
			out.State = "confirmed"
			out.Version = &version
		}
		encoded, e := marshalBounded(out, 2<<20)
		if e != nil {
			return e
		}
		return tx.Exec("INSERT INTO resource_group_requests(request_key,conversation_id,request_digest,result_json) VALUES(?,?,?,?)", key, conversation, digest, string(encoded)).Error
	})
	return
}

func confirmedResourceGroups(tx *gorm.DB, refs []string) (out map[string][]ResourceGroupSummary, err error) {
	out = map[string][]ResourceGroupSummary{}
	if len(refs) == 0 {
		return out, nil
	}
	var rows []struct {
		ResourceRef string
		ResourceGroupSummary
	}
	err = tx.Table("resource_group_members m").Select("m.resource_ref,h.group_id,h.version_id,h.name,h.revision,h.source_ref,h.account_ref,v.created_at AS confirmed_at").Joins("JOIN resource_group_heads h ON h.version_id=m.version_id").Joins("JOIN resource_group_versions v ON v.id=h.version_id").Where("m.resource_ref IN ?", refs).Order("h.name,h.group_id").Scan(&rows).Error
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		out[row.ResourceRef] = append(out[row.ResourceRef], row.ResourceGroupSummary)
	}
	return
}
