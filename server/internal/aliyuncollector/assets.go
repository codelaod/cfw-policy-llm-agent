package aliyuncollector

import (
	"encoding/json"
	"net/netip"
	"strconv"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

// collectAsset observes addresses and associated groups without inferring a
// listener, business role, authorization or NAT path from infrastructure data.
func (r *run) collectAsset(round int, id string, cfw []map[string]json.RawMessage) (domain.Asset, error) {
	cfg := r.collector.cfg
	region := cfg.AssetRegions[id]
	ids, _ := json.Marshal([]string{id})
	filters := map[string]string{"RegionId": region, "InstanceIds": string(ids), "MaxResults": strconv.Itoa(cfg.PageSize)}
	rows, complete, err := r.tokens(round, "DescribeInstances", filters, "Instances", "Instance", "InstanceId", 50)
	if err != nil {
		return domain.Asset{}, err
	}
	a := domain.Asset{ID: id, ScopeRef: cfg.Scope.Ref, Services: []domain.Service{}, Allowed: []domain.Access{}, Association: domain.Association{Status: "unknown"}}
	for _, row := range rows {
		if text(row, "InstanceId") != id || text(row, "RegionId") != region {
			return a, Failure("COLLECTOR_SCOPE_DENIED")
		}
		if err := checkAccount(row, cfg.ExpectedAccountID); err != nil {
			return a, err
		}
	}
	if len(rows) != 1 || !complete {
		return a, nil
	}
	row := rows[0]
	candidates := []string{}
	if cfg.Scope.Boundary == "internet" {
		for _, asset := range cfw {
			if value := text(asset, "InternetAddress"); value != "" {
				if parsed, ok := addresses(value); ok {
					candidates = append(candidates, parsed...)
				}
			}
		}
	} else {
		for _, parent := range []string{"InnerIpAddress", "VpcAttributes"} {
			nested, err := object(row[parent])
			if err != nil {
				continue
			}
			if parent == "VpcAttributes" {
				nested, err = object(nested["PrivateIpAddress"])
				if err != nil {
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
		if ip, err := netip.ParseAddr(candidates[0]); err == nil && ip.Is4() {
			a.Address = candidates[0]
			a.AddressObserved = true
		}
	}
	if raw, ok := row["SecurityGroupIds"]; ok {
		groups, err := object(raw)
		if err != nil {
			return a, Failure("PROVIDER_RESPONSE_INVALID")
		}
		ids, present := stringList(groups, "SecurityGroupId")
		if !present {
			return a, nil
		}
		ids = unique(ids)
		if len(ids) > 50 {
			return a, Failure("PROVIDER_RESPONSE_INVALID")
		}
		for _, group := range ids {
			if !alias(group) {
				return a, Failure("PROVIDER_RESPONSE_INVALID")
			}
			_, _, err := r.tokens(round, "DescribeSecurityGroupAttribute", map[string]string{"RegionId": region, "SecurityGroupId": group, "MaxResults": strconv.Itoa(cfg.PageSize)}, "Permissions", "Permission", "", 1000)
			if err != nil {
				return a, err
			}
		}
	}
	return a, nil
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
	seenIDs := map[string]bool{}
	token := ""
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
			if group := text(body, "SecurityGroupId"); group != filters["SecurityGroupId"] {
				return rows, false, Failure("COLLECTOR_SCOPE_DENIED")
			}
			if region := text(body, "RegionId"); region != filters["RegionId"] {
				return rows, false, Failure("COLLECTOR_SCOPE_DENIED")
			}
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
		if len(batch) > r.collector.cfg.PageSize || len(rows)+len(batch) > limit {
			return rows, false, nil
		}
		for _, row := range batch {
			if idKey != "" {
				id := text(row, idKey)
				if !alias(id) || seenIDs[id] {
					return rows, false, Failure("PROVIDER_IDENTITY_CONFLICT")
				}
				seenIDs[id] = true
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
