package storage

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/domain"
)

func exceptionsForAsset(in domain.Input, assetID string) []domain.Exception {
	values := []domain.Exception{}
	for _, value := range in.Baseline.Exceptions {
		if value.SubjectRef == assetID {
			values = append(values, value)
		}
	}
	return values
}

// This entry grants only selected-asset business exceptions. It deliberately
// cannot approve partition maintenance or turn insufficient NAT association
// evidence into an accepted fact.
func assetExceptionCheck(in domain.Input, value domain.Exception) bool {
	enabled := false
	for _, check := range in.Baseline.EnabledChecks {
		if check == value.CheckID {
			enabled = true
			break
		}
	}
	if !enabled {
		return false
	}
	if value.CheckID == "NAT-02" {
		return in.Scope.Boundary == "nat" && in.Scope.Direction == "out" && enum(value.Subcheck, "CHK-05", "OUT-01", "OUT-02") && (value.Subcheck != "CHK-05" || value.ServiceRef == "")
	}
	if value.Subcheck != "" {
		return false
	}
	if in.Scope.Boundary != "internet" {
		return false
	}
	switch value.CheckID {
	case "CHK-05":
		return value.ServiceRef == ""
	case "CHK-01", "CHK-03", "CHK-06", "CHK-07", "CHK-08":
		return in.Scope.Direction == "in"
	case "CHK-02":
		return in.Scope.Direction == "in" && value.ServiceRef == ""
	case "OUT-01", "OUT-02":
		return in.Scope.Direction == "out"
	default:
		return false
	}
}

func normalizeCandidateExceptions(values []domain.Exception, in domain.Input, assetID string, serviceIDs map[string]bool) error {
	if len(values) > 100 {
		return ErrInputInvalid
	}
	seen := map[string]bool{}
	for i := range values {
		value := &values[i]
		if value.SubjectRef != "" && value.SubjectRef != assetID {
			return ErrInputInvalid
		}
		if value.ScopeRef != "" && value.ScopeRef != in.Scope.Ref {
			return ErrInputInvalid
		}
		value.SubjectRef = assetID
		value.ScopeRef = in.Scope.Ref
		value.Confirmed = false
		if !assetExceptionCheck(in, *value) || (value.ServiceRef != "" && !serviceIDs[value.ServiceRef]) || len(value.ExpiresAt) > 80 || len(value.Reason) > 4096 || !utf8.ValidString(value.Reason) || len(value.AuthorizationSource) > 512 || !utf8.ValidString(value.AuthorizationSource) || strings.ContainsAny(value.AuthorizationSource, "\x00\r\n") {
			return ErrInputInvalid
		}
		value.Reason = strings.TrimSpace(value.Reason)
		value.AuthorizationSource = strings.TrimSpace(value.AuthorizationSource)
		identity := exceptionIdentity(*value)
		if seen[identity] {
			return ErrInputInvalid
		}
		seen[identity] = true
	}
	return nil
}

func exceptionIdentity(value domain.Exception) string {
	value.Confirmed = false
	value.ScopeRef = ""
	value.Reason = strings.TrimSpace(value.Reason)
	value.AuthorizationSource = strings.TrimSpace(value.AuthorizationSource)
	if at, err := time.Parse(time.RFC3339Nano, value.ExpiresAt); err == nil {
		value.ExpiresAt = at.UTC().Format(time.RFC3339Nano)
	}
	return requestDigest(value)
}

func replaceAssetExceptions(in *domain.Input, assetID string, values []domain.Exception, grant bool) error {
	asset := selectedAsset(in, assetID)
	if asset == nil {
		return ErrInputInvalid
	}
	kept := make([]domain.Exception, 0, len(in.Baseline.Exceptions)+len(values))
	for _, value := range in.Baseline.Exceptions {
		if value.SubjectRef != assetID {
			kept = append(kept, value)
		}
	}
	for _, value := range values {
		if value.SubjectRef != "" && value.SubjectRef != assetID {
			return ErrInputInvalid
		}
		if value.ServiceRef != "" && selectedService(asset, value.ServiceRef) == nil {
			return ErrInputInvalid
		}
		value.SubjectRef = assetID
		value.ScopeRef = in.Scope.Ref
		if grant {
			value.Confirmed = true
		}
		kept = append(kept, value)
	}
	in.Baseline.Exceptions = kept
	return nil
}

func validateProposedExceptions(ctx context.Context, base, candidate domain.Input, changes []AssetDraftChange, sections []string) error {
	if !containsSection(sections, "access") {
		return nil
	}
	current := map[string]bool{}
	for _, value := range base.Baseline.Exceptions {
		if value.Confirmed {
			current[exceptionIdentity(value)] = true
		}
	}
	at := time.Now().UTC()
	for _, change := range changes {
		if change.Access == nil {
			continue
		}
		if change.Access.ExceptionsDeclared != nil && !*change.Access.ExceptionsDeclared {
			continue
		}
		asset := selectedAsset(&candidate, change.AssetID)
		if asset == nil {
			return ErrInputInvalid
		}
		for _, value := range change.Access.Exceptions {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if value.SubjectRef != change.AssetID || value.ScopeRef != candidate.Scope.Ref || !assetExceptionCheck(candidate, value) || strings.TrimSpace(value.Reason) == "" || (value.ServiceRef != "" && selectedService(asset, value.ServiceRef) == nil) {
				return ErrInputInvalid
			}
			if !current[exceptionIdentity(value)] && missingExceptionAuthorizationSource(value.AuthorizationSource) {
				return ErrInputInvalid
			}
			expiry, err := time.Parse(time.RFC3339Nano, value.ExpiresAt)
			if err != nil {
				return ErrInputInvalid
			}
			// An unchanged historic exception can remain visibly expired. A new
			// or altered grant cannot be approved after its own expiry.
			if !expiry.After(at) && !current[exceptionIdentity(value)] {
				return ErrInputInvalid
			}
		}
	}
	return nil
}

// These exact placeholders explicitly say that no source has been supplied.
// This validates a declaration's presence; it does not verify a person, ticket
// system or approval authority, and leaves unchanged historic grants readable.
func missingExceptionAuthorizationSource(value string) bool {
	return enum(strings.ToLower(strings.TrimSpace(value)), "", "unknown", "declared_unknown", "未知", "不知道", "不清楚", "未确认", "待确认", "未核实", "待核实", "无", "无来源", "暂无", "none", "n/a", "na", "-", "--", "?", "？")
}

func intentFromInputAsset(in domain.Input, asset domain.Asset) intentPayload {
	payload := intentFromAsset(asset)
	payload.Exceptions = exceptionsForAsset(in, asset.ID)
	return payload
}
