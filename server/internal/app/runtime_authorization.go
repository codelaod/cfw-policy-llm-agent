package app

import (
	"context"
	"errors"

	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
)

// Shared authorization is durable. Adapter availability and private restrictions
// are a separate layer and never zero an omitted or temporarily blocked class.
func runtimeAuthorization(store *storage.Store, model modelconfig.Config, cloud cloudconfig.Config, execution workload.Config) (storage.BudgetPolicy, error) {
	ref := ""
	if execution.HasBudget() {
		ref = execution.Budget().Ref
	}
	for _, source := range []struct {
		configured, enabled bool
		ref                 string
	}{{model.Summary().Configured, model.RequestedEnabled(), model.Budget().Ref}, {cloud.Summary().Configured, cloud.RequestedEnabled(), cloud.Budget().Ref}} {
		if !source.configured || !source.enabled || source.ref == "" {
			continue
		}
		if ref != "" && ref != source.ref {
			return storage.BudgetPolicy{}, storage.ErrInputInvalid
		}
		ref = source.ref
	}
	if ref == "" {
		return storage.BudgetPolicy{}, nil
	}
	if execution.HasBudget() {
		b := execution.Budget()
		return storage.BudgetPolicy{Ref: ref, ApplicationCloudAttempts: b.ApplicationCloudAttempts, ApplicationModelAttempts: b.ApplicationModelAttempts, SessionCloudAttempts: b.SessionCloudAttempts, SessionModelAttempts: b.SessionModelAttempts, ExpiresAt: b.ExpiresAt}, nil
	}
	existing, e := store.GetBudgetPolicy(context.Background(), ref)
	if e == nil {
		return existing, nil
	}
	if !errors.Is(e, storage.ErrBudgetNotConfigured) {
		return existing, e
	}
	policy := storage.BudgetPolicy{Ref: ref}
	modelDeclared := model.Summary().Configured && model.RequestedEnabled() && model.Budget().Ref != ""
	if modelDeclared {
		b := model.Budget()
		policy.ApplicationModelAttempts = int64(b.ApplicationModelAttempts)
		policy.SessionModelAttempts = int64(b.SessionModelAttempts)
		policy.ExpiresAt = b.ExpiresAt
	}
	if cloud.Summary().Configured && cloud.RequestedEnabled() && cloud.Budget().Ref != "" {
		b := cloud.Budget()
		policy.ApplicationCloudAttempts = int64(b.ApplicationCloudAttempts)
		policy.SessionCloudAttempts = int64(b.SessionCloudAttempts)
		if cloud.ModelBudgetDeclared() {
			if modelDeclared {
				policy.ApplicationModelAttempts = min(policy.ApplicationModelAttempts, int64(b.ApplicationModelAttempts))
				policy.SessionModelAttempts = min(policy.SessionModelAttempts, int64(b.SessionModelAttempts))
			} else {
				policy.ApplicationModelAttempts = int64(b.ApplicationModelAttempts)
				policy.SessionModelAttempts = int64(b.SessionModelAttempts)
			}
		}
		var e error
		policy.ExpiresAt, e = earlierExpiry(policy.ExpiresAt, b.ExpiresAt)
		if e != nil {
			return policy, e
		}
	}
	return policy, nil
}
func adapterCeiling(ref string, application, session int, expiry string) *storage.AttemptCeiling {
	a, b := int64(application), int64(session)
	return &storage.AttemptCeiling{Ref: ref, ApplicationAttempts: &a, SessionAttempts: &b, ExpiresAt: expiry}
}
func modelAdapterCeiling(model modelconfig.Config, cloud cloudconfig.Config) *storage.AttemptCeiling {
	var out *storage.AttemptCeiling
	if model.Budget().Ref != "" {
		b := model.Budget()
		out = adapterCeiling(b.Ref, b.ApplicationModelAttempts, b.SessionModelAttempts, b.ExpiresAt)
	}
	if cloud.RequestedEnabled() && cloud.ModelBudgetDeclared() && cloud.Budget().Ref != "" {
		b := cloud.Budget()
		if out == nil {
			return adapterCeiling(b.Ref, b.ApplicationModelAttempts, b.SessionModelAttempts, b.ExpiresAt)
		}
		a, c := min(*out.ApplicationAttempts, int64(b.ApplicationModelAttempts)), min(*out.SessionAttempts, int64(b.SessionModelAttempts))
		out.ApplicationAttempts = &a
		out.SessionAttempts = &c
		out.ExpiresAt, _ = earlierExpiry(out.ExpiresAt, b.ExpiresAt)
	}
	return out
}
