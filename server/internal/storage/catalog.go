package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

// MaxResourceBindings is the current local plan/storage envelope, not a cloud or product capacity guarantee.
const MaxResourceBindings = 1000

type ResourceFilter struct {
	Tags         map[string]string `json:"tags,omitempty"`
	Regions      []string          `json:"regions,omitempty"`
	SourceRef    string            `json:"source_ref,omitempty"`
	AccountRef   string            `json:"account_ref,omitempty"`
	Region       string            `json:"region,omitempty"`
	ResourceType string            `json:"resource_type,omitempty"`
	ResourceID   string            `json:"resource_id,omitempty"`
	Name         string            `json:"name,omitempty"`
	Group        string            `json:"group,omitempty"`
}
type ResourcePage struct {
	ConfirmedGroups map[string][]ResourceGroupSummary `json:"confirmed_groups"`
	Items           []domain.Resource                 `json:"items"`
	NextCursor      string                            `json:"next_cursor"`
	HasMore         bool                              `json:"has_more"`
	Coverage        domain.CatalogueCoverage          `json:"coverage"`
}
type ResourceBinding struct {
	ResourceRef string `json:"resource_ref"`
	SnapshotID  string `json:"snapshot_id"`
	AssetID     string `json:"asset_id"`
	ScopeRef    string `json:"policy_scope_ref"`
	Boundary    string `json:"boundary"`
	Direction   string `json:"direction"`
}

func canonicalResourceID(r domain.Resource) string {
	id := r.ResourceID
	if id == "" {
		id = "artifact:" + r.IdentityContext + ":" + r.LocalAssetID
	}
	h := sha256.Sum256([]byte(strings.Join([]string{r.Provider, r.AccountRef, r.Region, r.ResourceType, id}, "\x00")))
	return "identity_" + hex.EncodeToString(h[:16])
}
func resourceID(r domain.Resource) string {
	h := sha256.Sum256([]byte(canonicalResourceID(r) + "\x00" + r.SourceRef))
	return "resource_" + hex.EncodeToString(h[:16])
}
func validateResource(r domain.Resource) bool {
	for _, v := range []string{r.Provider, r.AccountRef} {
		if !validKey(v) {
			return false
		}
	}
	for _, v := range []string{r.Name, r.Region, r.ResourceType, r.Group, r.OperatingSystem} {
		if len(v) > 512 || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return false
		}
	}
	_, err := time.Parse(time.RFC3339Nano, r.ObservedAt)
	return (validKey(r.ResourceID) || r.ResourceID == "" && validKey(r.LocalAssetID)) && err == nil && enum(r.Source, "synthetic", "imported", "live_readonly") && len(r.Tags) <= 100
}
func upsertResources(tx *gorm.DB, source string, resources []domain.Resource, coverage domain.CatalogueCoverage) error {
	if !validKey(source) {
		return ErrInputInvalid
	}
	for _, r := range resources {
		if !validateResource(r) {
			return ErrInputInvalid
		}
		r.SourceRef = source
		if r.IdentityKind == "" {
			if r.ResourceID != "" {
				r.IdentityKind = "provider_metadata"
			} else {
				r.IdentityKind = "artifact_local"
			}
		}
		r.IdentityRef = canonicalResourceID(r)
		r.ID = resourceID(r)
		var old struct{ Body string }
		res := tx.Raw("SELECT body FROM resource_catalog WHERE id=?", r.ID).Scan(&old)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			var prior domain.Resource
			if json.Unmarshal([]byte(old.Body), &prior) != nil {
				return ErrInputInvalid
			}
			at, _ := time.Parse(time.RFC3339Nano, r.ObservedAt)
			before, _ := time.Parse(time.RFC3339Nano, prior.ObservedAt)
			if before.After(at) {
				continue
			}
		}
		body, err := marshalBounded(r, 32768)
		if err != nil {
			return err
		}
		if err = tx.Exec(`INSERT INTO resource_catalog(id,source_ref,account_ref,region,resource_type,resource_id,name,body,observed_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET source_ref=excluded.source_ref,name=excluded.name,body=excluded.body,observed_at=excluded.observed_at`, r.ID, source, r.AccountRef, r.Region, r.ResourceType, resourceSearchID(r), r.Name, string(body), r.ObservedAt).Error; err != nil {
			return err
		}
	}
	body, err := marshalBounded(coverage, 16384)
	if err != nil {
		return err
	}
	if err = tx.Exec("INSERT INTO resource_catalogue_coverage(id,source_ref,body) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", requestDigest([]any{source, coverage.AccountRef, coverage.Selector}), source, string(body)).Error; err != nil {
		return err
	}
	r := tx.Exec("UPDATE resource_catalogue_state SET revision=revision+1 WHERE id=1")
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrInputInvalid
	}
	return nil
}
func (s *Store) UpsertResources(ctx context.Context, source string, resources []domain.Resource, coverage domain.CatalogueCoverage) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return upsertResources(tx, source, resources, coverage) })
}
func indexSnapshotResources(tx *gorm.DB, record SnapshotRecord) error {
	in := record.Input
	source := "local-offline"
	var configured string
	if err := tx.Raw("SELECT source_ref FROM configured_scope_catalog WHERE scope_ref=?", in.Scope.Ref).Scan(&configured).Error; err != nil {
		return err
	}
	if in.SourceKind == "live_readonly" && configured != "" {
		source = configured
	}
	at := in.CollectedAt
	if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
		at = record.CreatedAt
	}
	resources := []domain.Resource{}
	for _, a := range in.Assets {
		id := a.ProviderResourceID
		name := a.Name
		if name == "" {
			name = a.ID
		}
		r := domain.Resource{Provider: in.Scope.Provider, AccountRef: in.Scope.AccountRef, Region: a.Region, ResourceType: a.ResourceType, ResourceID: id, LocalAssetID: a.ID, IdentityContext: in.Scope.Ref, Name: name, Tags: a.Tags, OperatingSystem: a.OperatingSystem, Source: in.SourceKind, ObservedAt: at}
		r.SourceRef = source
		resources = append(resources, r)
	}
	if in.Selection.Kind == "gateway_policy" {
		r := domain.Resource{Provider: in.Scope.Provider, AccountRef: in.Scope.AccountRef, SourceRef: source, ResourceType: "nat_gateway", LocalAssetID: in.Scope.GatewayRef, IdentityContext: in.Scope.Ref, Name: in.Scope.GatewayRef, Source: in.SourceKind, ObservedAt: at}
		if in.GatewayMetadata != nil {
			if in.GatewayMetadata.ResourceType != "nat_gateway" || in.GatewayMetadata.ResourceID == "" || in.GatewayMetadata.Region == "" {
				return ErrInputInvalid
			}
			r = *in.GatewayMetadata
			r.Provider = in.Scope.Provider
			r.AccountRef = in.Scope.AccountRef
			r.SourceRef = source
			r.Source = in.SourceKind
			r.ObservedAt = at
		}
		resources = append(resources, r)
	}
	coverage := domain.CatalogueCoverage{Complete: false, ObservedAt: at, Source: source, Limitations: []string{"snapshot_only_not_account_inventory"}}
	if err := upsertResources(tx, source, resources, coverage); err != nil {
		return err
	}
	for i, r := range resources {
		asset := ""
		if i < len(in.Assets) {
			asset = in.Assets[i].ID
		}
		if err := tx.Exec("INSERT OR IGNORE INTO resource_snapshot_bindings(resource_ref,snapshot_id,asset_id,scope_ref,boundary,direction) VALUES(?,?,?,?,?,?)", resourceID(r), record.ID, asset, in.Scope.Ref, in.Scope.Boundary, in.Scope.Direction).Error; err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) SearchResources(ctx context.Context, filter ResourceFilter, cursor string, limit int) (page ResourcePage, err error) {
	page.Items = []domain.Resource{}
	page.ConfirmedGroups = map[string][]ResourceGroupSummary{}
	page.Coverage.Limitations = []string{}
	if limit < 1 || limit > 100 || len(filter.Regions) > 100 || len(filter.Tags) > 20 {
		return page, ErrInputInvalid
	}
	for _, v := range []string{filter.SourceRef, filter.AccountRef, filter.Region, filter.ResourceType, filter.ResourceID, filter.Name, filter.Group} {
		if len(v) > 512 || !utf8.ValidString(v) {
			return page, ErrInputInvalid
		}
	}
	type position struct {
		ID       string `json:"id"`
		Filter   string `json:"filter"`
		Revision int64  `json:"revision"`
	}
	pos := position{}
	if cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || len(raw) > 1024 || json.Unmarshal(raw, &pos) != nil || pos.Filter != requestDigest(filter) || pos.ID == "" {
			return page, ErrCursor
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state := tx.Raw("SELECT revision FROM resource_catalogue_state WHERE id=1").Scan(&page.Coverage.Revision)
		if state.Error != nil {
			return state.Error
		}
		if state.RowsAffected != 1 {
			return ErrInputInvalid
		}
		if cursor != "" && pos.Revision != page.Coverage.Revision {
			return ErrCursor
		}
		var confirmed *ResourceGroupSummary
		if filter.Group != "" {
			heads, e := resourceGroupHeads(tx, filter.Group, filter.SourceRef, filter.AccountRef)
			if e != nil {
				return e
			}
			if len(heads) == 0 {
				page.Coverage.Selector.Group = filter.Group
				page.Coverage.Limitations = []string{"GROUP_NOT_CONFIRMED"}
				return nil
			}
			if len(heads) != 1 {
				return ErrResourceGroupAmbiguous
			}
			confirmed = &heads[0]
		}
		q := tx.Table("resource_catalog").Select("body").Where("id > ?", pos.ID)
		for _, pair := range [][2]string{{"source_ref", filter.SourceRef}, {"account_ref", filter.AccountRef}, {"region", filter.Region}, {"resource_type", filter.ResourceType}, {"resource_id", filter.ResourceID}} {
			if pair[1] != "" {
				q = q.Where(pair[0]+" = ?", pair[1])
			}
		}
		if filter.Name != "" {
			q = q.Where("(name=? OR EXISTS(SELECT 1 FROM json_each(resource_catalog.body,'$.source_names') WHERE key IN ('instance_name','nat_gateway_name','firewall_name') AND value=?))", filter.Name, filter.Name)
		}
		if len(filter.Regions) > 0 {
			q = q.Where("region IN ?", filter.Regions)
		}
		for key, value := range filter.Tags {
			if len(key) > 128 || len(value) > 256 {
				return ErrInputInvalid
			}
			encoded, _ := json.Marshal(key)
			if value == "" {
				q = q.Where("json_type(body,?) IS NOT NULL", "$.tags."+string(encoded))
			} else {
				q = q.Where("json_extract(body,?)=?", "$.tags."+string(encoded), value)
			}
		}
		if confirmed != nil {
			q = q.Where("id IN (SELECT resource_ref FROM resource_group_members WHERE version_id=?)", confirmed.VersionID)
		}
		var rows []struct{ Body string }
		if e := q.Order("id").Limit(limit + 1).Scan(&rows).Error; e != nil {
			return e
		}
		page.HasMore = len(rows) > limit
		if page.HasMore {
			rows = rows[:limit]
		}
		for _, row := range rows {
			var r domain.Resource
			if e := json.Unmarshal([]byte(row.Body), &r); e != nil {
				return e
			}
			page.Items = append(page.Items, r)
		}
		refs := []string{}
		for _, r := range page.Items {
			refs = append(refs, r.ID)
		}
		groups, e := confirmedResourceGroups(tx, refs)
		if e != nil {
			return e
		}
		page.ConfirmedGroups = groups
		for i := range page.Items {
			r := &page.Items[i]
			r.Group = ""
			r.GroupConfirmed = false
			if confirmed != nil {
				r.Group = confirmed.Name
				r.GroupConfirmed = true
			} else if len(groups[r.ID]) == 1 {
				r.Group = groups[r.ID][0].Name
				r.GroupConfirmed = true
			}
		}
		if confirmed != nil {
			revision := page.Coverage.Revision
			page.Coverage = domain.CatalogueCoverage{AccountRef: confirmed.AccountRef, Selector: domain.ResourceQuery{Name: filter.Name, ResourceID: filter.ResourceID, Region: filter.Region, Regions: filter.Regions, ResourceType: filter.ResourceType, Group: filter.Group, Tags: filter.Tags}, Complete: !page.HasMore, ObservedAt: confirmed.ConfirmedAt, Source: confirmed.SourceRef, Limitations: []string{"CONFIRMED_GROUP_MEMBERSHIP_ONLY"}, Revision: revision}
		} else {
			page.Coverage.Source = filter.SourceRef
			page.Coverage.Complete = false
			var covers []struct{ Body string }
			c := tx.Table("resource_catalogue_coverage").Select("body")
			if filter.SourceRef != "" {
				c = c.Where("source_ref=?", filter.SourceRef)
			}
			if e := c.Scan(&covers).Error; e != nil {
				return e
			}
			for _, row := range covers {
				var v domain.CatalogueCoverage
				if e := json.Unmarshal([]byte(row.Body), &v); e != nil {
					return e
				}
				page.Coverage.Limitations = append(page.Coverage.Limitations, v.Limitations...)
				if page.Coverage.ObservedAt == "" || v.ObservedAt < page.Coverage.ObservedAt {
					page.Coverage.ObservedAt = v.ObservedAt
				}
			}
			// Broad existence cannot be proved by imported or snapshot-local membership.
			// Only a source-specific completed enumeration may make an empty result final.
			for _, row := range covers {
				var v domain.CatalogueCoverage
				if json.Unmarshal([]byte(row.Body), &v) != nil {
					return ErrInputInvalid
				}
				if filter.SourceRef != "" && coverageContains(v, filter) {
					page.Coverage.Complete = !page.HasMore
					break
				}
			}
		}
		if page.HasMore {
			b, _ := json.Marshal(position{ID: page.Items[len(page.Items)-1].ID, Filter: requestDigest(filter), Revision: page.Coverage.Revision})
			page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		}
		return nil
	})
	return
}
func (s *Store) ResourceBindings(ctx context.Context, refs []string, boundary, direction string) (rows []ResourceBinding, err error) {
	rows = []ResourceBinding{}
	if len(refs) < 1 || len(refs) > MaxResourceBindings {
		return rows, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Raw("SELECT b.* FROM resource_snapshot_bindings b JOIN snapshots s ON s.id=b.snapshot_id WHERE b.resource_ref IN ? AND b.boundary=? AND b.direction=? ORDER BY s.created_at DESC,s.id DESC,b.asset_id", refs, boundary, direction).Scan(&rows).Error
	return
}

func coverageContains(c domain.CatalogueCoverage, f ResourceFilter) bool {
	if len(f.Regions) > 1 {
		return false
	}
	if len(f.Regions) == 1 {
		f.Region = f.Regions[0]
	}
	if !c.Complete || c.AccountRef == "" || c.AccountRef != f.AccountRef || c.Selector.Region == "" || c.Selector.Region != f.Region || c.Selector.ResourceType == "" || c.Selector.ResourceType != f.ResourceType {
		return false
	}
	for key, value := range c.Selector.Tags {
		if f.Tags[key] != value {
			return false
		}
	}
	if len(c.Selector.ResourceIDs) > 0 {
		matched := false
		for _, id := range c.Selector.ResourceIDs {
			if f.ResourceID != "" && f.ResourceID == id {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	if c.Selector.Name != "" && c.Selector.Name != f.Name {
		return false
	}
	if c.Selector.ResourceID != "" && c.Selector.ResourceID != f.ResourceID {
		return false
	}
	if c.Selector.Group != "" && c.Selector.Group != f.Group {
		return false
	}
	return true
}

func resourceSearchID(r domain.Resource) string {
	if r.ResourceID != "" {
		return r.ResourceID
	}
	return r.LocalAssetID
}
