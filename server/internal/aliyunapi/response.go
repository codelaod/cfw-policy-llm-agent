package aliyunapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// sdkHTTP preserves the original bounded body before the generated SDK turns it
// into typed structs. Missing fields, [] and unknown conditions stay distinct.
type sdkHTTP struct {
	client    *http.Client
	raw       json.RawMessage
	failure   error
	sensitive []string
}

func (h *sdkHTTP) Call(req *http.Request, _ *http.Transport) (*http.Response, error) {
	resp, e := h.client.Do(req)
	if e != nil {
		h.failure = safeError(e)
		return nil, h.failure
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	resp.Body.Close()
	if e != nil {
		h.failure = Failure("CLOUD_RESPONSE_READ_FAILED")
		return nil, h.failure
	}
	if len(body) > MaxResponseBytes {
		h.failure = Failure("UPSTREAM_RESPONSE_TOO_LARGE")
		return nil, h.failure
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case 401, 403:
			h.failure = Failure("CLOUD_ACCESS_DENIED")
		case 429:
			h.failure = Failure("CLOUD_THROTTLED")
		default:
			h.failure = Failure("CLOUD_PROVIDER_UNAVAILABLE")
		}
		return nil, h.failure
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		obj, e := decodeObject(body)
		if e != nil {
			h.failure = Failure("CLOUD_RESPONSE_INVALID")
			return nil, h.failure
		}
		if code, ok := obj["Code"]; ok && code != "Success" && code != "200" {
			h.failure = Failure("CLOUD_PROVIDER_REJECTED")
			return nil, h.failure
		}
		if success, ok := obj["Success"]; ok && success != true {
			h.failure = Failure("CLOUD_PROVIDER_REJECTED")
			return nil, h.failure
		}
		if containsPrivate(obj, h.sensitive) {
			h.failure = Failure("CLOUD_RESPONSE_CONTAINS_CREDENTIAL")
			return nil, h.failure
		}
		h.raw = append(json.RawMessage(nil), body...)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}
func containsPrivate(value any, values []string) bool {
	switch v := value.(type) {
	case string:
		for _, s := range values {
			if s != "" && strings.Contains(v, s) {
				return true
			}
		}
	case json.Number:
		// Numeric application tokens are permitted by the local access policy.
		// Preserve the provider's lexical digits instead of passing through a
		// floating-point value that could lose a known credential's precision.
		return containsPrivate(v.String(), values)
	case []any:
		for _, child := range v {
			if containsPrivate(child, values) {
				return true
			}
		}
	case map[string]any:
		for k, child := range v {
			if containsPrivate(k, values) || containsPrivate(child, values) {
				return true
			}
		}
	}
	return false
}
func decodeObject(body []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 32 {
			return nil, Failure("CLOUD_RESPONSE_INVALID")
		}
		token, e := d.Token()
		if e != nil {
			return nil, e
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delim {
		case '{':
			out := map[string]any{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return nil, e
				}
				s, ok := key.(string)
				if !ok {
					return nil, Failure("CLOUD_RESPONSE_INVALID")
				}
				if _, present := out[s]; present {
					return nil, Failure("CLOUD_RESPONSE_INVALID")
				}
				v, e := read(depth + 1)
				if e != nil {
					return nil, e
				}
				out[s] = v
			}
			if _, e = d.Token(); e != nil {
				return nil, e
			}
			return out, nil
		case '[':
			out := []any{}
			for d.More() {
				v, e := read(depth + 1)
				if e != nil {
					return nil, e
				}
				out = append(out, v)
			}
			if _, e = d.Token(); e != nil {
				return nil, e
			}
			return out, nil
		}
		return nil, Failure("CLOUD_RESPONSE_INVALID")
	}
	value, e := read(0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, Failure("CLOUD_RESPONSE_INVALID")
	}
	out, ok := value.(map[string]any)
	if !ok {
		return nil, Failure("CLOUD_RESPONSE_INVALID")
	}
	return out, nil
}
func (r *Reader) accept(op string, q map[string]string, raw []byte) error {
	obj, e := decodeObject(raw)
	if e != nil {
		return Failure("CLOUD_RESPONSE_INVALID")
	}
	expected := r.factory.config.Connection.ExpectedAccountID()
	if op == "GetCallerIdentity" {
		r.verified = false
		account, ok := obj["AccountId"].(string)
		if !ok || account != expected {
			return Failure("CLOUD_ACCOUNT_MISMATCH")
		}
		kind, _ := obj["IdentityType"].(string)
		if kind != "RAMUser" && kind != "AssumedRoleUser" {
			return Failure("CLOUD_IDENTITY_REFUSED")
		}
		arn, _ := obj["Arn"].(string)
		if !validIdentityARN(kind, expected, arn) {
			return Failure("CLOUD_IDENTITY_INVALID")
		}
		r.verified = true
		return nil
	}
	var owner func(any) error
	owner = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			for k, item := range v {
				if k == "AliUid" || k == "MemberUid" || k == "OwnerId" {
					var account string
					switch n := item.(type) {
					case string:
						account = n
					case json.Number:
						if _, err := strconv.ParseUint(n.String(), 10, 64); err != nil {
							return Failure("CLOUD_OWNER_MISMATCH")
						}
						account = n.String()
					default:
						return Failure("CLOUD_OWNER_MISMATCH")
					}
					if account != expected {
						return Failure("CLOUD_OWNER_MISMATCH")
					}
				}
				if k == "NatGatewayId" && q["NatGatewayId"] != "" {
					if id, ok := item.(string); !ok || id != q["NatGatewayId"] {
						return Failure("CLOUD_GATEWAY_MISMATCH")
					}
				}
				if e := owner(item); e != nil {
					return e
				}
			}
		case []any:
			for _, item := range v {
				if e := owner(item); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e = owner(obj); e != nil {
		return e
	}

	if op == "DescribeAssetList" {
		region := r.instances[q["SearchItem"]]
		for _, asset := range r.factory.config.Connection.Assets() {
			if asset.InstanceID == q["SearchItem"] {
				region = asset.Region
			}
		}
		if values, present := obj["Assets"]; present {
			assets, ok := values.([]any)
			if !ok {
				return Failure("CLOUD_RESPONSE_INVALID")
			}
			for _, value := range assets {
				asset, ok := value.(map[string]any)
				if !ok {
					return Failure("CLOUD_RESPONSE_INVALID")
				}
				resource, _ := asset["ResourceInstanceId"].(string)
				bound, _ := asset["BindInstanceId"].(string)
				observed, _ := asset["RegionID"].(string)
				if (resource != q["SearchItem"] && bound != q["SearchItem"]) || observed != region {
					return Failure("CLOUD_ASSET_SCOPE_MISMATCH")
				}
			}
		}
	}
	if op == "DescribeInstances" {
		var requested []string
		json.Unmarshal([]byte(q["InstanceIds"]), &requested)
		allowed := map[string]bool{}
		for _, id := range requested {
			allowed[id] = true
		}
		container, _ := obj["Instances"].(map[string]any)
		instances, _ := container["Instance"].([]any)
		newGroups := map[string]bool{}
		newInstances := map[string]string{}
		seen := map[string]bool{}
		for _, item := range instances {
			instance, ok := item.(map[string]any)
			if !ok {
				return Failure("CLOUD_RESPONSE_INVALID")
			}
			id, ok := instance["InstanceId"].(string)
			if !ok || !r.factory.config.Connection.AllowsAsset(q["RegionId"], id) || len(requested) > 0 && !allowed[id] || seen[id] {
				return Failure("CLOUD_ASSET_SCOPE_MISMATCH")
			}
			seen[id] = true
			if region, ok := instance["RegionId"].(string); !ok || region != q["RegionId"] {
				return Failure("CLOUD_ASSET_SCOPE_MISMATCH")
			}
			if !instancePredicateMatches(instance, q) {
				continue
			}
			newInstances[id] = q["RegionId"]
			groups, _ := instance["SecurityGroupIds"].(map[string]any)
			ids, _ := groups["SecurityGroupId"].([]any)
			for _, item := range ids {
				group, ok := item.(string)
				if !ok || !idPattern.MatchString(group) {
					return Failure("CLOUD_RESPONSE_INVALID")
				}
				newGroups[q["RegionId"]+"/"+group] = true
			}
		}
		for id, region := range newInstances {
			r.instances[id] = region
		}
		for group := range newGroups {
			r.groups[group] = true
		}
	}
	if op == "DescribeNatFirewallList" {
		values, present := obj["NatFirewallList"]
		if present {
			rows, ok := values.([]any)
			if !ok {
				return Failure("CLOUD_RESPONSE_INVALID")
			}
			learned := map[string]string{}
			for _, item := range rows {
				row, ok := item.(map[string]any)
				if !ok {
					return Failure("CLOUD_RESPONSE_INVALID")
				}
				id, _ := row["NatGatewayId"].(string)
				region, _ := row["RegionId"].(string)
				if !r.factory.config.Connection.AllowsGateway(region, id) || q["RegionNo"] != "" && region != q["RegionNo"] {
					return Failure("CLOUD_GATEWAY_MISMATCH")
				}
				if q["ProxyName"] != "" {
					name, _ := row["ProxyName"].(string)
					if name != q["ProxyName"] {
						continue
					}
				}
				learned[id] = region
			}
			for id, region := range learned {
				r.gateways[id] = region
			}
		}
	}
	if op == "DescribeSecurityGroupAttribute" {
		if id, ok := obj["SecurityGroupId"].(string); !ok || id != q["SecurityGroupId"] {
			return Failure("CLOUD_GROUP_SCOPE_MISMATCH")
		}
	}
	return nil
}

// GetCallerIdentity calls the official assumed-role identity AssumedRoleUser.
// Both documented RAM ARN spellings keep the role and session in the resource;
// a root or another identity kind must not be accepted from an account prefix.
func validIdentityARN(kind, account, arn string) bool {
	prefix := "acs:ram::" + account + ":"
	if !strings.HasPrefix(arn, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(arn, prefix), "/")
	validPart := func(value string) bool {
		if len(value) == 0 || len(value) > 128 {
			return false
		}
		for _, c := range value {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '@' || c == '.' || c == '_' || c == '-') {
				return false
			}
		}
		return true
	}
	switch kind {
	case "RAMUser":
		return len(parts) == 2 && parts[0] == "user" && validPart(parts[1])
	case "AssumedRoleUser":
		return len(parts) == 3 && (parts[0] == "assumed-role" || parts[0] == "role") && validPart(parts[1]) && validPart(parts[2])
	}
	return false
}

func instancePredicateMatches(row map[string]any, q map[string]string) bool {
	if pattern := q["InstanceName"]; pattern != "" {
		name, _ := row["InstanceName"].(string)
		parts := strings.Split(pattern, "*")
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		if !regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(name) {
			return false
		}
	}
	tags := map[string]string{}
	container, _ := row["Tags"].(map[string]any)
	values, _ := container["Tag"].([]any)
	for _, item := range values {
		tag, ok := item.(map[string]any)
		if !ok {
			return false
		}
		key, _ := tag["TagKey"].(string)
		value, _ := tag["TagValue"].(string)
		if old, ok := tags[key]; ok && old != value {
			return false
		}
		tags[key] = value
	}
	for index := 1; index <= 20; index++ {
		key := q["Tag."+strconv.Itoa(index)+".Key"]
		if key == "" {
			continue
		}
		value, present := tags[key]
		if !present || q["Tag."+strconv.Itoa(index)+".Value"] != "" && value != q["Tag."+strconv.Itoa(index)+".Value"] {
			return false
		}
	}
	return true
}
