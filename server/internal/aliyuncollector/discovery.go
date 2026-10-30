package aliyuncollector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/workload"
)

// Selector is one accepted, finite metadata query. MaxObjects is a per-call
// output ceiling, never a limit on the persistent resource catalogue.
type Selector struct {
	Region      string            `json:"region"`
	Type        string            `json:"type"`
	Name        string            `json:"name,omitempty"`
	ResourceIDs []string          `json:"resource_ids,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	MaxPages    int               `json:"max_pages"`
	MaxObjects  int               `json:"max_objects"`
	Cursor      string            `json:"cursor,omitempty"`
}
type Discovery struct {
	Resources    []domain.Resource        `json:"resources"`
	Coverage     domain.CatalogueCoverage `json:"coverage"`
	NextCursor   string                   `json:"next_cursor,omitempty"`
	Exhausted    bool                     `json:"exhausted"`
	Observations []Observation            `json:"observations"`
	Artifacts    []RawArtifact            `json:"-"`
}
type discoveryCursor struct {
	Version  int    `json:"v"`
	Binding  string `json:"binding"`
	Batch    int    `json:"batch"`
	Token    string `json:"token,omitempty"`
	Page     int    `json:"page"`
	Skip     int    `json:"skip,omitempty"`
	PageHash string `json:"page_hash,omitempty"`
	PageSize int    `json:"page_size"`
	Count    int    `json:"count,omitempty"`
	Total    *int   `json:"total,omitempty"`
}

func metadataText(value string, max int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= max && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
func selectorBinding(cfg Config, s Selector) string {
	s.Cursor = ""
	s.MaxPages = 0
	s.MaxObjects = 0
	b, _ := json.Marshal(struct {
		Account, Source, Scope string
		Regions                []string
		Assets                 map[string]string
		Gateways               []string
		GatewayRegions         map[string]string
		Workload               workload.Resolved
		Selector               Selector
	}{cfg.ExpectedAccountID, cfg.SourceRef, cfg.Scope.Ref + "/" + cfg.Scope.AccountRef, cfg.AllowedRegions, cfg.AssetRegions, cfg.AllowedGatewayIDs, cfg.GatewayRegions, cfg.Workload, s})
	return hash(b)
}
func encodeDiscoveryCursor(c discoveryCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func nameMatches(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	parts := strings.Split(pattern, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(value)
}
func stringMetadataKnown(row map[string]json.RawMessage, key string) bool {
	raw, present := row[key]
	var value *string
	return present && json.Unmarshal(raw, &value) == nil && value != nil
}
func metadataMatches(resource domain.Resource, s Selector) bool {
	matchName := func(value string) bool {
		if s.Type == "nat_gateway" {
			return s.Name == "" || s.Name == value
		}
		return nameMatches(s.Name, value)
	}
	nameMatched := matchName(resource.Name)
	if !nameMatched {
		for _, value := range resource.SourceNames {
			if matchName(value) {
				nameMatched = true
				break
			}
		}
	}
	if !nameMatched {
		return false
	}
	for key, value := range s.Tags {
		got, present := resource.Tags[key]
		if !present || value != "" && got != value {
			return false
		}
	}
	return true
}

// Discover is separate from New: looking up a bounded NAT name must not require
// knowing its gateway ID first. The caller supplies an identity-verified Reader
// and owns source references, persistence and any later execution proposal.
func Discover(ctx context.Context, reader Reader, cfg Config, s Selector) (out Discovery, err error) {
	out.Resources = []domain.Resource{}
	if ctx == nil || reader == nil || !decimal(cfg.ExpectedAccountID) {
		return out, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	cfg, err = resolveConfig(cfg)
	if err != nil {
		return out, err
	}
	if len(cfg.AllowedRegions) == 0 || !allowedRegion(cfg, s.Region) || (s.Type != "ecs" && s.Type != "nat_gateway") || s.MaxPages <= 0 || s.MaxObjects <= 0 || s.MaxPages > workload.MaxCounter || s.MaxObjects > workload.MaxCounter || !metadataText(s.Name, 128) || len(s.Tags) > 20 {
		return out, Failure("COLLECTOR_SCOPE_DENIED")
	}
	if s.Type == "nat_gateway" && (len(s.Tags) > 0 || strings.Contains(s.Name, "*")) {
		return out, Failure("COLLECTOR_SCOPE_DENIED")
	}
	frozenTags := map[string]string{}
	for key, value := range s.Tags {
		frozenTags[key] = value
	}
	s.Tags = frozenTags
	frozenAssets := map[string]string{}
	for id, region := range cfg.AssetRegions {
		frozenAssets[id] = region
	}
	cfg.AssetRegions = frozenAssets
	cfg.AllowedGatewayIDs = append([]string(nil), cfg.AllowedGatewayIDs...)
	s.ResourceIDs = append([]string(nil), s.ResourceIDs...)
	sort.Strings(s.ResourceIDs)
	seenRequested := map[string]bool{}
	for _, id := range s.ResourceIDs {
		prefix := "i-"
		if s.Type == "nat_gateway" {
			prefix = "ngw-"
		}
		if !alias(id) || !strings.HasPrefix(id, prefix) || seenRequested[id] {
			return out, Failure("COLLECTOR_SCOPE_DENIED")
		}
		seenRequested[id] = true
		if s.Type == "ecs" && len(cfg.AssetRegions) > 0 && cfg.AssetRegions[id] != s.Region {
			return out, Failure("COLLECTOR_SCOPE_DENIED")
		}
		if s.Type == "nat_gateway" && !gatewayAllowed(cfg, id, s.Region) {
			return out, Failure("COLLECTOR_SCOPE_DENIED")
		}
	}
	for key, value := range s.Tags {
		if key == "" || !metadataText(key, 128) || !metadataText(value, 128) {
			return out, Failure("COLLECTOR_SCOPE_DENIED")
		}
	}
	if s.Type == "ecs" && len(s.ResourceIDs) == 0 && len(cfg.AssetRegions) > 0 {
		for id, region := range cfg.AssetRegions {
			if region == s.Region {
				s.ResourceIDs = append(s.ResourceIDs, id)
			}
		}
		sort.Strings(s.ResourceIDs)
		if len(s.ResourceIDs) == 0 {
			return out, Failure("COLLECTOR_SCOPE_DENIED")
		}
	}
	if s.Type == "nat_gateway" && len(s.ResourceIDs) == 0 && (len(cfg.AllowedGatewayIDs) > 0 || len(cfg.GatewayRegions) > 0) {
		if len(cfg.GatewayRegions) > 0 {
			for id, region := range cfg.GatewayRegions {
				if region == s.Region && gatewayAllowed(cfg, id, s.Region) {
					s.ResourceIDs = append(s.ResourceIDs, id)
				}
			}
		} else {
			s.ResourceIDs = append([]string(nil), cfg.AllowedGatewayIDs...)
		}
		sort.Strings(s.ResourceIDs)
		if len(s.ResourceIDs) == 0 {
			return out, Failure("COLLECTOR_SCOPE_DENIED")
		}
	}
	maxPages := s.MaxPages
	if cfg.MaxAttempts < maxPages {
		maxPages = cfg.MaxAttempts
	}
	maxObjects := s.MaxObjects
	if cfg.Workload.Limits.AssetsPerBatch < maxObjects {
		maxObjects = cfg.Workload.Limits.AssetsPerBatch
	}
	if maxPages <= 0 || maxObjects <= 0 {
		return out, Failure("COLLECTION_ATTEMPT_LIMIT")
	}
	binding := selectorBinding(cfg, s)
	cursor := discoveryCursor{Version: 1, Binding: binding, Page: 1, PageSize: ecsPageSize(cfg)}
	if s.Type == "nat_gateway" {
		cursor.PageSize = cfg.PageSize
	}
	continued := s.Cursor != ""
	if continued {
		if len(s.Cursor) > 16384 {
			return out, Failure("DISCOVERY_CURSOR_INVALID")
		}
		raw, e := base64.RawURLEncoding.DecodeString(s.Cursor)
		if e == nil {
			_, e = object(raw)
		}
		if e != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || cursor.Binding != binding || cursor.Batch < 0 || cursor.Count < 0 || cursor.Count > int(^uint(0)>>1)-100 || cursor.Page < 1 || cursor.Page > workload.MaxCounter || cursor.Skip < 0 || cursor.Skip > 100 || cursor.PageSize < 1 || cursor.PageSize > 100 || len(cursor.Token) > 4096 {
			return out, Failure("DISCOVERY_CURSOR_INVALID")
		}
		if cursor.Skip > 0 && cursor.PageHash == "" || cursor.Skip == 0 && cursor.PageHash != "" || s.Type == "ecs" && (cursor.PageSize < 10 || cursor.Page != 1 || cursor.Token == "" && cursor.Count != 0) || cursor.Total != nil && *cursor.Total < 0 {
			return out, Failure("DISCOVERY_CURSOR_INVALID")
		}
		if s.Type == "nat_gateway" && cursor.PageSize > 50 {
			return out, Failure("DISCOVERY_CURSOR_INVALID")
		}
	}
	window, _ := workload.Seconds(cfg.Workload.Limits.CollectionSeconds)
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	run := &run{collector: &Collector{reader: reader, cfg: cfg}, ctx: ctx}
	out.Coverage = domain.CatalogueCoverage{AccountRef: cfg.Scope.AccountRef, Selector: domain.ResourceQuery{Name: s.Name, Region: s.Region, ResourceType: s.Type, ResourceIDs: append([]string(nil), s.ResourceIDs...), Tags: s.Tags}, Source: "live_readonly", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{}}
	if s.Type == "nat_gateway" {
		out.Coverage.Limitations = append(out.Coverage.Limitations, "NAT_DIRECTORY_CFW_MANAGED_ONLY")
	}
	if len(s.ResourceIDs) == 1 {
		out.Coverage.Selector.ResourceID = s.ResourceIDs[0]
	}
	if continued {
		out.Coverage.Limitations = append(out.Coverage.Limitations, "DISCOVERY_CONTINUATION")
	}

	defer func() {
		out.Artifacts = run.artifacts
		out.Observations = run.observations
		out.Coverage.Limitations = unique(out.Coverage.Limitations)
	}()
	batches := [][]string{{}}
	if len(s.ResourceIDs) > 0 {
		batches = nil
		step := 100
		if s.Type == "nat_gateway" {
			step = 1
		}
		for i := 0; i < len(s.ResourceIDs); i += step {
			end := i + step
			if end > len(s.ResourceIDs) {
				end = len(s.ResourceIDs)
			}
			batches = append(batches, s.ResourceIDs[i:end])
		}
	}
	if cursor.Batch >= len(batches) {
		return out, Failure("DISCOVERY_CURSOR_INVALID")
	}
	seen := map[string]bool{}
	seenTokens := map[string]bool{}
	if cursor.Token != "" {
		seenTokens[cursor.Token] = true
	}
	predicateIncomplete := false
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		query := map[string]string{}
		op := "DescribeInstances"
		if s.Type == "ecs" {
			query["RegionId"] = s.Region
			query["MaxResults"] = strconv.Itoa(cursor.PageSize)
			if cursor.Token != "" {
				query["NextToken"] = cursor.Token
			}
			if len(batches[cursor.Batch]) > 0 {
				encoded, _ := json.Marshal(batches[cursor.Batch])
				query["InstanceIds"] = string(encoded)
			} else {
				if s.Name != "" {
					query["InstanceName"] = s.Name
				}
				keys := []string{}
				for key := range s.Tags {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for i, key := range keys {
					prefix := "Tag." + strconv.Itoa(i+1)
					query[prefix+".Key"] = key
					if s.Tags[key] != "" {
						query[prefix+".Value"] = s.Tags[key]
					}
				}
			}
		} else {
			op = "DescribeNatFirewallList"
			query = map[string]string{"RegionNo": s.Region, "PageNo": strconv.Itoa(cursor.Page), "PageSize": strconv.Itoa(cursor.PageSize)}
			if len(batches[cursor.Batch]) > 0 {
				query["NatGatewayId"] = batches[cursor.Batch][0]
			}
			// A generic name may refer to NatGatewayName or ProxyName. Only a
			// finite RegionNo/ID query can preserve both source fields; applying
			// ProxyName here would falsely exhaust native gateway-name matches.
		}
		obs := Observation{Round: 0, Operation: op, Filters: cloneQuery(query), StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Pages: 1}
		raw, e := run.read(0, op, query)
		if e != nil {
			obs.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			run.observations = append(run.observations, obs)
			out.Coverage.Limitations = append(out.Coverage.Limitations, controlled(e).Error())
			out.NextCursor = encodeDiscoveryCursor(cursor)
			return out, e
		}
		body, e := object(raw)
		if e != nil {
			return out, e
		}
		var rows []map[string]json.RawMessage
		present := false
		if s.Type == "ecs" {
			container, exists := body["Instances"]
			if exists {
				nested, e := object(container)
				if e != nil {
					return out, e
				}
				rows, present, e = list(nested, "Instance")
				if e != nil {
					return out, e
				}
			}
		} else {
			rows, present, e = list(body, "NatFirewallList")
			if e != nil {
				return out, e
			}
		}
		if !present {
			out.Coverage.Limitations = append(out.Coverage.Limitations, "DISCOVERY_ENUMERATION_UNVERIFIED")
			obs.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			run.observations = append(run.observations, obs)
			return out, nil
		}
		if len(rows) > cursor.PageSize {
			return out, Failure("PROVIDER_RESPONSE_INVALID")
		}
		pageHash := rowFingerprint(rows, nil)
		if cursor.Skip > len(rows) || cursor.PageHash != "" && cursor.PageHash != pageHash {
			return out, Failure("DISCOVERY_CURSOR_STALE")
		}
		converted := []domain.Resource{}
		knownPredicates := []bool{}
		pageIDs := map[string]bool{}
		for _, row := range rows {
			if e := checkAccount(row, cfg.ExpectedAccountID); e != nil {
				return out, e
			}
			resource := domain.Resource{}
			if s.Type == "ecs" {
				id, region := text(row, "InstanceId"), text(row, "RegionId")
				if !alias(id) || !strings.HasPrefix(id, "i-") || region != s.Region {
					return out, Failure("COLLECTOR_SCOPE_DENIED")
				}
				if len(batches[cursor.Batch]) > 0 {
					found := false
					for _, allowed := range batches[cursor.Batch] {
						found = found || id == allowed
					}
					if !found {
						return out, Failure("COLLECTOR_SCOPE_DENIED")
					}
				}
				tags, e := instanceTags(row)
				if e != nil {
					return out, e
				}
				names := map[string]string{}
				if value := text(row, "InstanceName"); value != "" {
					names["instance_name"] = value
				}
				resource = domain.Resource{IdentityKind: "provider_metadata", Provider: "aliyun", AccountRef: cfg.Scope.AccountRef, ResourceID: id, Name: text(row, "InstanceName"), SourceNames: names, Region: region, ResourceType: "ecs", Tags: tags, OperatingSystem: text(row, "OSType"), Source: "live_readonly", ObservedAt: obs.StartedAt}
			} else {
				resource = gatewayResource(row, cfg, obs.StartedAt)
				if !alias(resource.ResourceID) || !strings.HasPrefix(resource.ResourceID, "ngw-") || resource.Region != s.Region {
					return out, Failure("COLLECTOR_SCOPE_DENIED")
				}
				if len(batches[cursor.Batch]) > 0 && resource.ResourceID != batches[cursor.Batch][0] {
					return out, Failure("COLLECTOR_SCOPE_DENIED")
				}

			}
			if pageIDs[resource.ResourceID] {
				return out, Failure("PROVIDER_IDENTITY_CONFLICT")
			}
			pageIDs[resource.ResourceID] = true
			known := true
			if s.Name != "" {
				if s.Type == "ecs" {
					known = stringMetadataKnown(row, "InstanceName")
				} else {
					_, gatewayName := row["NatGatewayName"]
					_, proxyName := row["ProxyName"]
					known = (gatewayName || proxyName) && (!gatewayName || stringMetadataKnown(row, "NatGatewayName")) && (!proxyName || stringMetadataKnown(row, "ProxyName"))
				}
			}
			if len(s.Tags) > 0 && resource.Tags == nil {
				known = false
			}
			if !known {
				predicateIncomplete = true
				out.Coverage.Limitations = append(out.Coverage.Limitations, "RESOURCE_FILTER_UNVERIFIED")
			}
			knownPredicates = append(knownPredicates, known)
			converted = append(converted, resource)
		}
		nextToken := ""
		knownEnd := true
		totalKnown := true
		if s.Type == "ecs" {
			value, exists := body["NextToken"]
			if !exists {
				knownEnd = false
			} else if json.Unmarshal(value, &nextToken) != nil || len(nextToken) > 4096 {
				return out, Failure("PROVIDER_RESPONSE_INVALID")
			}
			if _, exists := body["TotalCount"]; exists {
				total, known := totalCount(body, false)
				if !known || cursor.Total != nil && *cursor.Total != total {
					totalKnown = false
				} else {
					cursor.Total = &total
				}
			}
			if cursor.Total != nil && (cursor.Count+len(rows) > *cursor.Total || nextToken == "" && cursor.Count+len(rows) != *cursor.Total) {
				totalKnown = false
			}
			if nextToken != "" {
				if seenTokens[nextToken] {
					knownEnd = false
				} else {
					seenTokens[nextToken] = true
				}
			}
		} else {
			total, known := totalCount(body, false)
			if !known || cursor.Total != nil && *cursor.Total != total {
				totalKnown = false
				knownEnd = false
			} else {
				cursor.Total = &total
			}
			if cursor.Total != nil && ((cursor.Page-1)*cursor.PageSize+len(rows) > *cursor.Total) {
				knownEnd = false
				totalKnown = false
			}
			if rawPage, ok := body["PageNo"]; ok && strings.Trim(string(rawPage), "\"") != strconv.Itoa(cursor.Page) {
				knownEnd = false
			}
		}
		obs.SemanticSHA256 = pageHash
		obs.EnumerationComplete = knownEnd && totalKnown
		obs.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		run.observations = append(run.observations, obs)
		start := cursor.Skip
		for index := start; index < len(converted); index++ {
			resource := converted[index]
			if seen[resource.ResourceID] {
				return out, Failure("PROVIDER_IDENTITY_CONFLICT")
			}
			seen[resource.ResourceID] = true
			if !knownPredicates[index] || metadataMatches(resource, s) {
				out.Resources = append(out.Resources, resource)
			}
			if len(out.Resources) >= maxObjects && index+1 < len(converted) {
				cursor.Skip = index + 1
				cursor.PageHash = pageHash
				out.NextCursor = encodeDiscoveryCursor(cursor)
				out.Coverage.Limitations = append(out.Coverage.Limitations, "DISCOVERY_OBJECT_LIMIT")
				return out, nil
			}
		}
		cursor.Skip = 0
		cursor.PageHash = ""
		cursor.Count += len(rows)
		if !knownEnd || !totalKnown {
			out.Coverage.Limitations = append(out.Coverage.Limitations, "DISCOVERY_ENUMERATION_UNVERIFIED")
			return out, nil
		}
		more := false
		if s.Type == "ecs" {
			more = nextToken != ""
			if more && len(rows) == 0 {
				out.Coverage.Limitations = append(out.Coverage.Limitations, "DISCOVERY_ENUMERATION_UNVERIFIED")
				return out, nil
			}
			cursor.Token = nextToken
		} else {
			more = cursor.Total != nil && cursor.Page*cursor.PageSize < *cursor.Total
			if more && len(rows) != cursor.PageSize {
				out.Coverage.Limitations = append(out.Coverage.Limitations, "DISCOVERY_ENUMERATION_UNVERIFIED")
				return out, nil
			}
			cursor.Page++
		}
		if !more {
			cursor.Batch++
			cursor.Token = ""
			cursor.Page = 1
			cursor.Count = 0
			cursor.Total = nil
			if cursor.Batch >= len(batches) {
				out.Exhausted = true
				out.Coverage.Complete = !continued && !predicateIncomplete
				return out, nil
			}
		}
		if len(out.Resources) >= maxObjects {
			out.NextCursor = encodeDiscoveryCursor(cursor)
			out.Coverage.Limitations = append(out.Coverage.Limitations, "DISCOVERY_OBJECT_LIMIT")
			return out, nil
		}
	}
	out.NextCursor = encodeDiscoveryCursor(cursor)
	out.Coverage.Limitations = append(out.Coverage.Limitations, "DISCOVERY_PAGE_LIMIT")
	return out, nil
}
func (c *Collector) Discover(ctx context.Context, s Selector) (Discovery, error) {
	return Discover(ctx, c.reader, c.cfg, s)
}
