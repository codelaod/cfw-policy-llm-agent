package storage

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

// AuditDiscoveryRecord is a preparation-stage observation, not a sealed audit
// input or a completed task. Unknown predicate metadata remains explicit.
// Raw provider responses exist only in audit_discovery.raw_artifacts and are
// deliberately absent from this type and every normal read projection.
type AuditDiscoveryRecord struct {
	ID                     string                     `json:"id"`
	JobID                  string                     `json:"job_id"`
	CreatedAt              string                     `json:"created_at"`
	Resources              []domain.Resource          `json:"resources" gorm:"-"`
	Coverages              []domain.CatalogueCoverage `json:"coverages" gorm:"-"`
	UnverifiedResourceRefs []string                   `json:"unverified_resource_refs" gorm:"-"`
	Complete               bool                       `json:"complete"`
	Code                   string                     `json:"code,omitempty"`
	SourceRef              string                     `json:"-"`
	ConfigRevision         string                     `json:"-"`
	Body                   string                     `json:"-"`
	RequestDigest          string                     `json:"-"`
}

func auditDiscovery(tx *gorm.DB, jobID string) (*AuditDiscoveryRecord, error) {
	var row struct{ ID, JobID, CreatedAt, SourceRef, ConfigRevision, Body, RequestDigest string }
	r := tx.Raw("SELECT id,job_id,created_at,source_ref,config_revision,body,request_digest FROM audit_discovery WHERE job_id=?", jobID).Scan(&row)
	if r.Error != nil {
		return nil, r.Error
	}
	if r.RowsAffected == 0 {
		return nil, nil
	}
	var result AuditDiscoveryRecord
	d := json.NewDecoder(strings.NewReader(row.Body))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || result.ID != row.ID || result.JobID != row.JobID || result.CreatedAt != row.CreatedAt {
		return nil, ErrInputInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, ErrInputInvalid
	}
	result.SourceRef, result.ConfigRevision, result.Body, result.RequestDigest = row.SourceRef, row.ConfigRevision, row.Body, row.RequestDigest
	return &result, nil
}

func (s *Store) GetAuditDiscovery(ctx context.Context, jobID string) (*AuditDiscoveryRecord, error) {
	if !attemptAlias(jobID) {
		return nil, ErrInputInvalid
	}
	return auditDiscovery(s.db.WithContext(ctx), jobID)
}

// SaveAuditDiscovery saves one whole bounded directory round. Identical replay
// returns its original record; a different payload cannot overwrite that round.
// The original plan/intent, sealed input, terminal state and budget are untouched.
func (s *Store) SaveAuditDiscovery(ctx context.Context, jobID string, resources []domain.Resource, coverages []domain.CatalogueCoverage, raw json.RawMessage, code string) (record AuditDiscoveryRecord, err error) {
	if !attemptAlias(jobID) || code != "" && !cloudCode(code) {
		return record, ErrInputInvalid
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`[]`)
	}
	if len(raw) > 32<<20 || !json.Valid(raw) {
		return record, ErrInputInvalid
	}
	if resources == nil {
		resources = []domain.Resource{}
	}
	if coverages == nil {
		coverages = []domain.CatalogueCoverage{}
	}
	request := struct {
		Resources []domain.Resource
		Coverages []domain.CatalogueCoverage
		Raw       json.RawMessage
		Code      string
	}{resources, coverages, raw, code}
	requestBody, err := marshalBounded(request, 40<<20)
	if err != nil {
		return record, err
	}
	digest := requestDigest(json.RawMessage(requestBody))
	// Detach every metadata map/slice and the private artifact buffer.
	request.Resources, request.Coverages, request.Raw = nil, nil, nil
	if json.Unmarshal(requestBody, &request) != nil {
		return record, ErrInputInvalid
	}
	resources, coverages, raw, code = request.Resources, request.Coverages, request.Raw, request.Code
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, jobID)
		if e != nil {
			return e
		}
		old, e := auditDiscovery(tx, jobID)
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
		if job.Kind != "audit" || !enum(job.Status, "running", "cancelling") {
			return ErrJobTerminal
		}
		plan, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		grant, e := validatePreparationGrant(tx, plan)
		if e != nil {
			return e
		}
		if plan.ConsumedJobID != jobID || plan.ConversationID != job.ConversationID || plan.BudgetRef != job.BudgetRef || !reflect.DeepEqual(plan.Workload, job.Workload) {
			return ErrInputInvalid
		}
		var accepted preparedAcceptance
		r := tx.Raw("SELECT * FROM prepared_audit_acceptance WHERE job_id=? AND plan_id=?", jobID, plan.ID).Scan(&accepted)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 || accepted.AcceptedAt != job.CreatedAt || accepted.RequestDigest != requestDigest(plan.Body) {
			return ErrInputInvalid
		}
		artifactRegions, e := validateDiscoveryRaw(raw, plan, grant)
		if e != nil {
			return e
		}
		normalized, observed, unverified, complete, e := normalizeAuditDiscovery(plan, grant, resources, coverages, artifactRegions)
		if e != nil {
			return e
		}
		if job.Status == "cancelling" {
			complete = false
			if code == "" {
				code = "CANCELLED"
			}
			for i := range observed {
				observed[i].Complete = false
				observed[i].Limitations = discoveryLimitation(observed[i].Limitations, "DISCOVERY_CANCELLED")
			}
		}
		if code != "" {
			complete = false
			if code != "DISCOVERY_NO_MATCH" {
				for i := range observed {
					observed[i].Complete = false
					observed[i].Limitations = discoveryLimitation(observed[i].Limitations, code)
				}
			}
		}
		for _, coverage := range observed {
			region := preparationRegions(coverage.Selector)[0]
			items := []domain.Resource{}
			for _, resource := range normalized {
				if resource.Region == region {
					items = append(items, resource)
				}
			}
			if e = upsertResources(tx, plan.DataSourceID, items, coverage); e != nil {
				return e
			}
		}
		id, e := opaqueID("discovery")
		if e != nil {
			return e
		}
		record = AuditDiscoveryRecord{ID: id, JobID: jobID, CreatedAt: now(), Resources: normalized, Coverages: observed, UnverifiedResourceRefs: unverified, Complete: complete, Code: code, SourceRef: plan.DataSourceID, ConfigRevision: plan.ConfigRevision, RequestDigest: digest}
		body, e := marshalBounded(record, 5<<20)
		if e != nil {
			return e
		}
		record.Body = string(body)
		if e = tx.Exec("INSERT INTO audit_discovery(id,job_id,created_at,source_ref,config_revision,body,raw_artifacts,request_digest) VALUES(?,?,?,?,?,?,?,?)", id, jobID, record.CreatedAt, plan.DataSourceID, plan.ConfigRevision, record.Body, string(raw), digest).Error; e != nil {
			return e
		}
		return appendEvent(tx, &job, "stage_changed", map[string]any{"phase": "L1", "stage": "discovery_saved", "discovery_ref": id, "complete": complete, "code": code})
	})
	return record, err
}

func discoveryLimitation(values []string, code string) []string {
	if !containsPrepared(values, code) {
		values = append(values, code)
	}
	return values
}

func normalizeAuditDiscovery(plan ExecutionPlan, grant ConnectionGrant, resources []domain.Resource, coverages []domain.CatalogueCoverage, artifactRegions map[string]bool) ([]domain.Resource, []domain.CatalogueCoverage, []string, bool, error) {
	spec := plan.Preparation
	regions := preparationRegions(spec.Query)
	perRegion := spec.ResultLimit + 1
	if perRegion > plan.Workload.Limits.AssetsPerBatch {
		perRegion = plan.Workload.Limits.AssetsPerBatch
	}
	if len(coverages) > len(regions) || len(resources) > len(regions)*perRegion {
		return nil, nil, nil, false, ErrInputInvalid
	}
	byRegion := map[string]int{}
	complete := len(coverages) == len(regions)
	for i := range coverages {
		coverage := &coverages[i]
		q := coverage.Selector
		actualRegions := preparationRegions(q)
		if len(actualRegions) != 1 || !containsPrepared(regions, actualRegions[0]) || q.ResourceType != spec.Query.ResourceType || q.Name != spec.Query.Name || q.Group != "" || !reflect.DeepEqual(nonNilTags(q.Tags), nonNilTags(spec.Query.Tags)) || coverage.AccountRef != grant.AccountRef || !enum(coverage.Source, "live_readonly", plan.DataSourceID) || coverage.Revision < 0 || len(coverage.Limitations) > 100 {
			return nil, nil, nil, false, ErrInputInvalid
		}
		region := actualRegions[0]
		if _, exists := byRegion[region]; exists {
			return nil, nil, nil, false, ErrInputInvalid
		}
		if _, e := time.Parse(time.RFC3339Nano, coverage.ObservedAt); e != nil {
			return nil, nil, nil, false, ErrInputInvalid
		}
		for _, limitation := range coverage.Limitations {
			if !cloudCode(limitation) {
				return nil, nil, nil, false, ErrInputInvalid
			}
		}
		if q.ResourceID != "" && len(q.ResourceIDs) != 0 && (len(q.ResourceIDs) != 1 || q.ResourceIDs[0] != q.ResourceID) {
			return nil, nil, nil, false, ErrInputInvalid
		}
		ids := preparationIDs(q)
		if !finiteKeys(ids, 10000, false) {
			return nil, nil, nil, false, ErrInputInvalid
		}
		if len(preparationIDs(spec.Query)) > 0 && len(ids) == 0 {
			return nil, nil, nil, false, ErrInputInvalid
		}
		for _, id := range ids {
			if !discoveryIDAllowed(spec, grant, region, id) {
				return nil, nil, nil, false, ErrInputInvalid
			}
		}
		coverage.Selector.Region, coverage.Selector.Regions = region, nil
		coverage.Selector.ResourceID, coverage.Selector.ResourceIDs = "", append([]string{}, ids...)
		if len(ids) == 1 {
			coverage.Selector.ResourceID = ids[0]
			coverage.Selector.ResourceIDs = nil
		}
		coverage.Limitations = append([]string{}, coverage.Limitations...)
		if len(coverage.Limitations) != 0 {
			coverage.Complete = false
		}
		if !artifactRegions[region] {
			coverage.Complete = false
			coverage.Limitations = discoveryLimitation(coverage.Limitations, "DISCOVERY_EVIDENCE_MISSING")
		}
		for _, id := range ids {
			if !artifactRegions["all\x00"+region] && !artifactRegions["id\x00"+region+"\x00"+id] {
				coverage.Complete = false
				coverage.Limitations = discoveryLimitation(coverage.Limitations, "DISCOVERY_EVIDENCE_MISSING")
			}
		}
		complete = complete && coverage.Complete && len(coverage.Limitations) == 0
		byRegion[region] = i
	}
	seen := map[string]bool{}
	unverified := []string{}
	for i := range resources {
		resource := &resources[i]
		index, covered := byRegion[resource.Region]
		if !covered || !artifactRegions[resource.Region] || resource.Provider != "aliyun" || resource.AccountRef != grant.AccountRef || resource.ResourceType != spec.Query.ResourceType || !attemptAlias(resource.ResourceID) || !containsPrepared(grant.Regions, resource.Region) || resource.Source != "live_readonly" || resource.SourceRef != "" && resource.SourceRef != plan.DataSourceID || !discoveryIDAllowed(spec, grant, resource.Region, resource.ResourceID) || !validateResource(*resource) {
			return nil, nil, nil, false, ErrInputInvalid
		}
		if !artifactRegions["all\x00"+resource.Region] && !artifactRegions["id\x00"+resource.Region+"\x00"+resource.ResourceID] {
			return nil, nil, nil, false, ErrInputInvalid
		}
		key := resource.Region + "\x00" + resource.ResourceID
		if seen[key] {
			return nil, nil, nil, false, ErrInputInvalid
		}
		seen[key] = true
		if resource.Group != "" || resource.GroupConfirmed || resource.LocalAssetID != "" || resource.IdentityContext != "" {
			return nil, nil, nil, false, ErrInputInvalid
		}
		for key, value := range resource.SourceNames {
			if !enum(key, "instance_name", "nat_gateway_name", "firewall_name") || !discoveryText(value, 512) {
				return nil, nil, nil, false, ErrInputInvalid
			}
		}
		for key, value := range resource.Tags {
			if !discoveryText(key, 256) || key == "" || !discoveryText(value, 256) {
				return nil, nil, nil, false, ErrInputInvalid
			}
		}
		verified := true
		if spec.Query.Name != "" && !preparedNameMatches(spec.Query.Name, *resource) {
			if resource.Name != "" || len(resource.SourceNames) != 0 {
				return nil, nil, nil, false, ErrInputInvalid
			}
			verified = false
		}
		for key, value := range spec.Query.Tags {
			actual, present := resource.Tags[key]
			if !present {
				if resource.Tags != nil {
					return nil, nil, nil, false, ErrInputInvalid
				}
				verified = false
			} else if value != "" && actual != value {
				return nil, nil, nil, false, ErrInputInvalid
			}
		}
		resource.SourceRef = plan.DataSourceID
		resource.IdentityKind = "provider_metadata"
		resource.IdentityRef = canonicalResourceID(*resource)
		resource.ID = resourceID(*resource)
		if !verified {
			unverified = append(unverified, resource.ID)
			coverages[index].Complete = false
			coverages[index].Limitations = discoveryLimitation(coverages[index].Limitations, "RESOURCE_FILTER_UNVERIFIED")
			complete = false
		}
	}
	sort.Strings(unverified)
	return resources, coverages, unverified, complete, nil
}

func nonNilTags(tags map[string]string) map[string]string {
	if tags == nil {
		return map[string]string{}
	}
	return tags
}

func discoveryText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func discoveryIDAllowed(spec *PreparationSpec, grant ConnectionGrant, region, id string) bool {
	if len(preparationIDs(spec.Query)) != 0 && !containsPrepared(preparationIDs(spec.Query), id) {
		return false
	}
	allowed := grant.AssetIDs
	if spec.Query.ResourceType == "nat_gateway" {
		allowed = grant.GatewayIDs
	}
	return (len(allowed) == 0 || containsPrepared(allowed, id)) && preparedGrantResource(grant, PreparedResource{ResourceID: id, Region: region, ResourceType: spec.Query.ResourceType})
}

func validateDiscoveryRaw(raw json.RawMessage, plan ExecutionPlan, grant ConnectionGrant) (map[string]bool, error) {
	var artifacts []CloudRawArtifact
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&artifacts) != nil || artifacts == nil || len(artifacts) > plan.EffectCeiling.CloudHTTPAttempts {
		return nil, ErrInputInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, ErrInputInvalid
	}
	spec := plan.Preparation
	regions := map[string]bool{}
	probe := make([]CloudRawArtifact, len(artifacts))
	for i, artifact := range artifacts {
		if artifact.Round != 0 {
			return nil, ErrInputInvalid
		}
		if len(artifact.Query) > 50 {
			return nil, ErrInputInvalid
		}
		region := ""
		queriedIDs := []string{}
		if spec.Query.ResourceType == "ecs" && artifact.Operation == "DescribeInstances" {
			region = artifact.Query["RegionId"]
			ids := []string{}
			if encoded := artifact.Query["InstanceIds"]; encoded != "" {
				if json.Unmarshal([]byte(encoded), &ids) != nil || !finiteKeys(ids, 100, true) {
					return nil, ErrInputInvalid
				}
			}
			if len(ids) == 0 && (len(preparationIDs(spec.Query)) != 0 || len(grant.AssetIDs) != 0 || len(grant.Assets) != 0) {
				return nil, ErrInputInvalid
			}
			for _, id := range ids {
				if !discoveryIDAllowed(spec, grant, region, id) {
					return nil, ErrInputInvalid
				}
			}
			queriedIDs = ids
			if len(ids) == 0 {
				if artifact.Query["InstanceName"] != spec.Query.Name {
					return nil, ErrInputInvalid
				}
				tags := map[string]string{}
				for key, value := range artifact.Query {
					if strings.HasPrefix(key, "Tag.") && strings.HasSuffix(key, ".Key") {
						prefix := strings.TrimSuffix(key, ".Key")
						if _, duplicate := tags[value]; duplicate {
							return nil, ErrInputInvalid
						}
						tags[value] = artifact.Query[prefix+".Value"]
					}
				}
				if !reflect.DeepEqual(tags, nonNilTags(spec.Query.Tags)) {
					return nil, ErrInputInvalid
				}
			}
		} else if spec.Query.ResourceType == "nat_gateway" && artifact.Operation == "DescribeNatFirewallList" {
			region = artifact.Query["RegionNo"]
			if id := artifact.Query["NatGatewayId"]; id != "" {
				if !discoveryIDAllowed(spec, grant, region, id) {
					return nil, ErrInputInvalid
				}
				queriedIDs = []string{id}
			} else if len(preparationIDs(spec.Query)) != 0 || len(grant.GatewayIDs) != 0 || len(grant.Gateways) != 0 {
				return nil, ErrInputInvalid
			}
			if name := artifact.Query["ProxyName"]; name != "" && name != spec.Query.Name {
				return nil, ErrInputInvalid
			}
		} else {
			return nil, ErrInputInvalid
		}
		if !containsPrepared(preparationRegions(spec.Query), region) || !containsPrepared(grant.Regions, region) {
			return nil, ErrInputInvalid
		}
		for key, value := range artifact.Query {
			if len(key) > 64 || len(value) > 8192 {
				return nil, ErrInputInvalid
			}
			if enum(key, "MaxResults", "PageNo", "PageSize") {
				if n, e := strconv.Atoi(value); e != nil || n < 1 {
					return nil, ErrInputInvalid
				}
			}
			if artifact.Operation == "DescribeInstances" && strings.HasPrefix(key, "Tag.") {
				parts := strings.Split(key, ".")
				if len(parts) != 3 || !enum(parts[2], "Key", "Value") {
					return nil, ErrInputInvalid
				}
				n, e := strconv.Atoi(parts[1])
				if e != nil || n < 1 || n > 20 || artifact.Query["Tag."+parts[1]+".Key"] == "" {
					return nil, ErrInputInvalid
				}
			} else if artifact.Operation == "DescribeInstances" && !enum(key, "RegionId", "MaxResults", "NextToken", "InstanceIds", "InstanceName") || artifact.Operation == "DescribeNatFirewallList" && !enum(key, "RegionNo", "NatGatewayId", "ProxyName", "PageNo", "PageSize") {
				return nil, ErrInputInvalid
			}
		}
		regions[region] = true
		if len(queriedIDs) == 0 {
			regions["all\x00"+region] = true
		} else {
			for _, id := range queriedIDs {
				regions["id\x00"+region+"\x00"+id] = true
			}
		}
		probe[i] = artifact
		probe[i].Round = 1 // validator probe only; raw Round 0 is persisted intact.
		// The accepted discovery selector above validates up to twenty tag pairs;
		// the generic artifact helper's legacy twenty-field query cap does not
		// describe this API. Its body/base64/hash checks remain shared.
		probe[i].Query = nil
	}
	encoded, _ := json.Marshal(probe)
	if !privateCloudArtifacts(encoded) {
		return nil, ErrInputInvalid
	}
	return regions, nil
}
