package aliyuncollector

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strconv"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

// collectInstances keeps provider identity checks inside each region/ID batch.
// The per-round maps are discarded before the second observation, so no fetch
// or group evidence is silently reused across the consistency barrier.
func (r *run) collectInstances(round int, ids []string) (bool, error) {
	cfg := r.collector.cfg
	regions := map[string][]string{}
	for _, id := range ids {
		regions[cfg.AssetRegions[id]] = append(regions[cfg.AssetRegions[id]], id)
	}
	names := []string{}
	for region := range regions {
		names = append(names, region)
	}
	sort.Strings(names)
	allComplete := true
	for _, region := range names {
		selected := regions[region]
		sort.Strings(selected)
		for start := 0; start < len(selected); start += 100 {
			end := start + 100
			if end > len(selected) {
				end = len(selected)
			}
			batch := selected[start:end]
			encoded, _ := json.Marshal(batch)
			rows, complete, err := r.tokens(round, "DescribeInstances", map[string]string{"RegionId": region, "InstanceIds": string(encoded), "MaxResults": strconv.Itoa(ecsPageSize(cfg))}, "Instances", "Instance", "InstanceId", len(batch))
			if err != nil {
				return false, err
			}
			allowed := map[string]bool{}
			for _, id := range batch {
				allowed[id] = true
			}
			for _, row := range rows {
				id := text(row, "InstanceId")
				if !allowed[id] || text(row, "RegionId") != region {
					return false, Failure("COLLECTOR_SCOPE_DENIED")
				}
				if err := checkAccount(row, cfg.ExpectedAccountID); err != nil {
					return false, err
				}
				if _, exists := r.instances[id]; exists {
					return false, Failure("PROVIDER_IDENTITY_CONFLICT")
				}
				r.instances[id] = row
				r.instanceComplete[id] = complete
			}
			for _, id := range batch {
				if _, exists := r.instances[id]; !exists {
					complete = false
					r.instanceComplete[id] = false
				}
			}
			if !complete {
				allComplete = false
			}
		}
	}
	return allComplete, nil
}

// collectAsset attaches observed infrastructure only. Unknown fields stay
// unknown; a tag, OS or group permission never confirms a business service.
func (r *run) collectAsset(round int, id string, cfw []map[string]json.RawMessage) (domain.Asset, error) {
	cfg := r.collector.cfg
	region := cfg.AssetRegions[id]
	a := domain.Asset{ID: id, ScopeRef: cfg.Scope.Ref, Services: []domain.Service{}, Allowed: []domain.Access{}, Association: domain.Association{Status: "unknown"}, SecurityGroupCollection: "not_collected"}
	row, exists := r.instances[id]
	if !exists {
		return a, nil
	}
	a.Name = text(row, "InstanceName")
	a.ProviderResourceID = id
	a.Region = region
	a.ResourceType = "ecs"
	a.OperatingSystem = text(row, "OSType")
	if a.OperatingSystem == "" {
		a.OperatingSystem = text(row, "OSName")
	}
	a.MetadataComplete = r.instanceComplete[id]
	tags, err := instanceTags(row)
	if err != nil {
		return a, err
	}
	a.Tags = tags
	if !a.MetadataComplete {
		return a, nil
	}
	candidates := []string{}
	if cfg.Scope.Boundary == "internet" {
		for _, asset := range cfw {
			if parsed, ok := addresses(text(asset, "InternetAddress")); ok {
				candidates = append(candidates, parsed...)
			}
		}
	} else {
		for _, parent := range []string{"InnerIpAddress", "VpcAttributes"} {
			nested, e := object(row[parent])
			if e != nil {
				continue
			}
			if parent == "VpcAttributes" {
				nested, e = object(nested["PrivateIpAddress"])
				if e != nil {
					continue
				}
			}
			values, present := stringList(nested, "IpAddress")
			if present {
				for _, value := range values {
					if parsed, ok := addresses(value); ok {
						candidates = append(candidates, parsed...)
					}
				}
			}
		}
	}
	candidates = unique(candidates)
	if len(candidates) == 1 {
		if ip, e := netip.ParseAddr(candidates[0]); e == nil && ip.Is4() {
			a.Address = candidates[0]
			a.AddressObserved = true
		}
	}
	raw, ok := row["SecurityGroupIds"]
	if !ok {
		return a, nil
	}
	groups, e := object(raw)
	if e != nil {
		return a, Failure("PROVIDER_RESPONSE_INVALID")
	}
	groupIDs, present := stringList(groups, "SecurityGroupId")
	if !present {
		return a, nil
	}
	groupIDs = unique(groupIDs)
	if len(groupIDs) > r.collector.cfg.Workload.Limits.MaxPolicies {
		return a, Failure("PROVIDER_RESPONSE_INVALID")
	}
	a.SecurityGroupCollection = "complete"
	a.SecurityGroups = []domain.SecurityGroupEvidence{}
	for _, group := range groupIDs {
		if !alias(group) {
			return a, Failure("PROVIDER_RESPONSE_INVALID")
		}
		key := region + "/" + group
		evidence, cached := r.groups[key]
		if !cached {
			rows, complete, e := r.tokens(round, "DescribeSecurityGroupAttribute", map[string]string{"RegionId": region, "SecurityGroupId": group, "MaxResults": strconv.Itoa(groupPageSize(cfg)), "Direction": "all"}, "Permissions", "Permission", "", cfg.MaxPolicies)
			if fatal(controlled(e)) {
				return a, e
			}
			evidence = domain.SecurityGroupEvidence{ID: group, Region: region, Complete: complete && e == nil, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Permissions: []domain.SecurityGroupPermission{}, Limitations: []string{}}
			for _, permission := range rows {
				evidence.Permissions = append(evidence.Permissions, securityGroupPermission(permission))
			}
			if !evidence.Complete {
				evidence.Limitations = append(evidence.Limitations, "SECURITY_GROUP_ENUMERATION_INCOMPLETE")
			}
			r.groups[key] = evidence
			if e != nil {
				a.SecurityGroupCollection = "partial"
				a.SecurityGroups = append(a.SecurityGroups, evidence)
				return a, e
			}
		}
		a.SecurityGroups = append(a.SecurityGroups, evidence)
		if !evidence.Complete {
			a.SecurityGroupCollection = "partial"
		}
	}
	if raw, present := row["NetworkInterfaces"]; present {
		container, e := object(raw)
		partial := e != nil
		if !partial {
			interfaces, known, e := list(container, "NetworkInterface")
			partial = e != nil || !known || len(interfaces) > 1
		}
		if partial {
			a.SecurityGroupCollection = "partial"
			for i := range a.SecurityGroups {
				a.SecurityGroups[i].Limitations = append(a.SecurityGroups[i].Limitations, "MULTI_NIC_ASSOCIATION_NOT_VERIFIED")
			}
		}
	}
	return a, nil
}
func instanceTags(row map[string]json.RawMessage) (map[string]string, error) {
	raw, present := row["Tags"]
	if !present {
		return nil, nil
	}
	container, e := object(raw)
	if e != nil {
		return nil, Failure("PROVIDER_RESPONSE_INVALID")
	}
	rows, present, e := list(container, "Tag")
	if e != nil {
		return nil, e
	}
	if !present {
		return nil, nil
	}
	tags := map[string]string{}
	for _, tag := range rows {
		key := text(tag, "TagKey")
		value := text(tag, "TagValue")
		if _, present := tag["TagValue"]; !present {
			return nil, nil
		}
		var typedValue string
		if json.Unmarshal(tag["TagValue"], &typedValue) != nil {
			return nil, Failure("PROVIDER_RESPONSE_INVALID")
		}
		if key == "" || !metadataText(key, 128) || !metadataText(value, 128) {
			return nil, Failure("PROVIDER_RESPONSE_INVALID")
		}
		if old, ok := tags[key]; ok && old != value {
			return nil, Failure("PROVIDER_IDENTITY_CONFLICT")
		}
		tags[key] = value
	}
	return tags, nil
}
func securityGroupPermission(row map[string]json.RawMessage) domain.SecurityGroupPermission {
	p := domain.SecurityGroupPermission{Direction: text(row, "Direction"), Action: text(row, "Policy"), Protocol: text(row, "IpProtocol"), Source: text(row, "SourceCidrIp"), Destination: text(row, "DestCidrIp"), PortRange: text(row, "PortRange"), Priority: text(row, "Priority"), Conditions: map[string]string{}}
	for field, target := range map[string]*string{"Direction": &p.Direction, "Policy": &p.Action, "IpProtocol": &p.Protocol, "SourceCidrIp": &p.Source, "DestCidrIp": &p.Destination, "PortRange": &p.PortRange, "Priority": &p.Priority} {
		if raw, present := row[field]; present {
			var textValue string
			if json.Unmarshal(raw, &textValue) != nil {
				p.Conditions[field] = semantic(raw)
				*target = "unknown"
			}
		}
	}
	if p.Direction == "" {
		p.Direction = "unknown"
	}
	if p.Action == "" {
		p.Action = "unknown"
	}
	if p.Protocol == "" {
		p.Protocol = "unknown"
	}
	for key, value := range row {
		switch key {
		case "Direction", "Policy", "IpProtocol", "SourceCidrIp", "DestCidrIp", "PortRange", "Priority":
			continue
		}
		p.Conditions[key] = semantic(value)
	}
	return p
}
func gatewayResource(row map[string]json.RawMessage, cfg Config, at string) domain.Resource {
	name := text(row, "NatGatewayName")
	if name == "" {
		name = text(row, "ProxyName")
	}
	names := map[string]string{}
	if value := text(row, "NatGatewayName"); value != "" {
		names["nat_gateway_name"] = value
	}
	if value := text(row, "ProxyName"); value != "" {
		names["firewall_name"] = value
	}
	return domain.Resource{IdentityKind: "provider_metadata", Provider: "aliyun", AccountRef: cfg.Scope.AccountRef, ResourceID: text(row, "NatGatewayId"), Name: name, SourceNames: names, Region: text(row, "RegionId"), ResourceType: "nat_gateway", Source: "live_readonly", ObservedAt: at}
}

func (r *run) tokens(round int, op string, filters map[string]string, parent, array, idKey string, limit int) (rows []map[string]json.RawMessage, complete bool, err error) {
	obs := Observation{Round: round, Operation: op, Filters: cloneQuery(filters), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	extras := []string{}
	defer func() {
		obs.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		obs.EnumerationComplete = complete
		obs.SemanticSHA256 = rowFingerprint(rows, extras)
		r.observations = append(r.observations, obs)
	}()
	seenTokens := map[string]bool{}
	rowPages := map[string]int{}
	seenIDs := map[string]bool{}
	token := ""
	pageSize, _ := strconv.Atoi(filters["MaxResults"])
	if pageSize <= 0 {
		return rows, false, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	expectedTotal := -1
	for page := 0; page <= limit; page++ {
		q := cloneQuery(filters)
		if token != "" {
			q["NextToken"] = token
		}
		raw, e := r.read(round, op, q)
		if e != nil {
			return rows, false, e
		}
		obs.Pages++
		body, e := object(raw)
		if e != nil {
			return rows, false, e
		}
		if op == "DescribeSecurityGroupAttribute" {
			if text(body, "SecurityGroupId") == "" || text(body, "RegionId") == "" {
				return rows, false, nil
			}
			if text(body, "SecurityGroupId") != filters["SecurityGroupId"] || text(body, "RegionId") != filters["RegionId"] {
				return rows, false, Failure("COLLECTOR_SCOPE_DENIED")
			}
		}
		if _, ok := body["TotalCount"]; ok {
			total, known := totalCount(body, false)
			if !known || expectedTotal >= 0 && expectedTotal != total {
				return rows, false, nil
			}
			expectedTotal = total
		}
		for key, value := range body {
			if key != parent && key != "NextToken" && key != "TotalCount" && key != "RequestId" && key != "PageSize" && key != "PageNumber" && key != "MaxResults" {
				extras = append(extras, key+":"+semantic(value))
			}
		}
		envelope, exists := body[parent]
		if !exists {
			return rows, false, nil
		}
		nested, e := object(envelope)
		if e != nil {
			return rows, false, e
		}
		batch, present, e := list(nested, array)
		if e != nil {
			return rows, false, e
		}
		if !present {
			return rows, false, nil
		}
		if len(batch) > pageSize {
			return rows, false, nil
		}
		if op == "DescribeInstances" {
			var requested []string
			if json.Unmarshal([]byte(filters["InstanceIds"]), &requested) != nil {
				return rows, false, Failure("PROVIDER_RESPONSE_INVALID")
			}
			allowed := map[string]bool{}
			for _, id := range requested {
				allowed[id] = true
			}
			for _, row := range batch {
				if !allowed[text(row, "InstanceId")] || text(row, "RegionId") != filters["RegionId"] {
					return rows, false, Failure("COLLECTOR_SCOPE_DENIED")
				}
			}
		}
		for _, row := range batch {
			if idKey == "" {
				encoded, _ := json.Marshal(row)
				key := semantic(encoded)
				if previous, exists := rowPages[key]; exists && previous != page {
					return rows, false, nil
				}
				rowPages[key] = page
			}
			if idKey != "" {
				id := text(row, idKey)
				if !alias(id) || seenIDs[id] {
					return rows, false, Failure("PROVIDER_IDENTITY_CONFLICT")
				}
				seenIDs[id] = true
			}
			if len(rows) >= limit {
				return rows, false, nil
			}
			rows = append(rows, row)
		}
		nextRaw, present := body["NextToken"]
		if !present {
			return rows, false, nil
		}
		var next string
		if json.Unmarshal(nextRaw, &next) != nil || len(next) > 4096 {
			return rows, false, Failure("PROVIDER_RESPONSE_INVALID")
		}
		if next == "" {
			if expectedTotal >= 0 && expectedTotal != len(rows) {
				return rows, false, nil
			}
			return rows, true, nil
		}
		if seenTokens[next] || len(batch) == 0 {
			return rows, false, nil
		}
		seenTokens[next] = true
		token = next
	}
	return rows, false, nil
}
