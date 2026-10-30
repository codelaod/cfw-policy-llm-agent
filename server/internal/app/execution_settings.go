package app

import (
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
	"time"
)

// BindExecutionSettings combines explicitly selected sources without inventing
// a grant. Existing private zero ceilings and expiry remain stricter settings.
func BindExecutionSettings(model modelconfig.Config, cloud cloudconfig.Config, execution workload.Config) (modelconfig.Config, cloudconfig.Config, error) {
	if !execution.Configured() {
		return model, cloud, nil
	}
	if profile := execution.ModelProviderProfile(); profile != "" && model.Summary().Configured {
		if model.Summary().SourceKind != "env_file" && model.ProviderProfile() != profile {
			return model, cloud, storage.ErrConflict
		}
		var e error
		model, e = model.WithProviderProfile(profile)
		if e != nil {
			return model, cloud, e
		}
	}
	if !execution.HasBudget() {
		return model, cloud, nil
	}
	b := execution.Budget()
	if model.Summary().Configured && model.RequestedEnabled() {
		current := model.Budget()
		if current.Ref != "" && current.Ref != b.Ref {
			return model, cloud, storage.ErrConflict
		}
		next := modelconfig.Budget{Ref: b.Ref, ExpiresAt: b.ExpiresAt, ApplicationModelAttempts: int(b.ApplicationModelAttempts), SessionModelAttempts: int(b.SessionModelAttempts)}
		if current.Ref != "" {
			next.ApplicationModelAttempts = min(next.ApplicationModelAttempts, current.ApplicationModelAttempts)
			next.SessionModelAttempts = min(next.SessionModelAttempts, current.SessionModelAttempts)
			var e error
			next.ExpiresAt, e = earlierExpiry(next.ExpiresAt, current.ExpiresAt)
			if e != nil {
				return model, cloud, e
			}
		}
		var e error
		model, e = model.WithBudget(next)
		if e != nil {
			return model, cloud, e
		}
	}
	if cloud.Summary().Configured && cloud.RequestedEnabled() {
		current := cloud.Budget()
		if current.Ref != "" && current.Ref != b.Ref {
			return model, cloud, storage.ErrConflict
		}
		next := cloudconfig.Budget{Ref: b.Ref, ExpiresAt: b.ExpiresAt, ApplicationCloudAttempts: int(b.ApplicationCloudAttempts), SessionCloudAttempts: int(b.SessionCloudAttempts), ApplicationModelAttempts: int(b.ApplicationModelAttempts), SessionModelAttempts: int(b.SessionModelAttempts)}
		if current.Ref != "" {
			next.ApplicationCloudAttempts = min(next.ApplicationCloudAttempts, current.ApplicationCloudAttempts)
			next.SessionCloudAttempts = min(next.SessionCloudAttempts, current.SessionCloudAttempts)
			next.ApplicationModelAttempts = min(next.ApplicationModelAttempts, current.ApplicationModelAttempts)
			next.SessionModelAttempts = min(next.SessionModelAttempts, current.SessionModelAttempts)
			var e error
			next.ExpiresAt, e = earlierExpiry(next.ExpiresAt, current.ExpiresAt)
			if e != nil {
				return model, cloud, e
			}
		}
		var e error
		cloud, e = cloud.WithBudget(next)
		if e != nil {
			return model, cloud, e
		}
	}
	return model, cloud, nil
}
func earlierExpiry(left, right string) (string, error) {
	if left == "" {
		return right, nil
	}
	if right == "" {
		return left, nil
	}
	a, e := time.Parse(time.RFC3339Nano, left)
	if e != nil {
		return "", storage.ErrInputInvalid
	}
	b, e := time.Parse(time.RFC3339Nano, right)
	if e != nil {
		return "", storage.ErrInputInvalid
	}
	if b.Before(a) {
		return right, nil
	}
	return left, nil
}
