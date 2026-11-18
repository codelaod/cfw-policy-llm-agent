package storage

import (
	"context"
	"path/filepath"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func TestE4ConfiguredScopeCatalogIsLocalAndKeepsHistoricalScope(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalog.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in, _ := audit.SyntheticExample("internet-in")
	ctx := context.Background()
	if err = s.RegisterConfiguredScopes(ctx, "explicit-source", "config-v1", []domain.Scope{in.Scope}); err != nil {
		t.Fatal(err)
	}
	scopes, err := s.PolicyScopes(ctx, 0, 100)
	if err != nil || len(scopes) != 1 || scopes[0] != in.Scope {
		t.Fatal("local catalog unavailable", err)
	}
	if _, err = s.CreateConversation(ctx, "catalog-only", in.Scope.Ref); err != nil {
		t.Fatal("catalog scope not usable locally", err)
	}
	var budgets, attempts int64
	s.db.Raw("SELECT count(*) FROM budget_policies").Scan(&budgets)
	s.db.Raw("SELECT count(*) FROM outbound_attempts").Scan(&attempts)
	if budgets != 0 || attempts != 0 {
		t.Fatal("registration created authorization or usage")
	}
	if _, err = s.SaveSnapshot(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterConfiguredScopes(ctx, "explicit-source", "config-v2", nil); err != nil {
		t.Fatal(err)
	}
	scopes, err = s.PolicyScopes(ctx, 0, 100)
	if err != nil || len(scopes) != 1 {
		t.Fatal("inactive catalog hid historical snapshot", err)
	}
	changed := in.Scope
	changed.AccountRef = "different-account"
	if err = s.RegisterConfiguredScopes(ctx, "explicit-source", "config-v3", []domain.Scope{changed}); err == nil {
		t.Fatal("same ref changed identity")
	}
	if err = s.RegisterConfiguredScopes(ctx, "other-source", "config-v3", []domain.Scope{in.Scope}); err == nil {
		t.Fatal("same ref moved sources")
	}
	if err = s.RegisterConfiguredScopes(ctx, "explicit-source", "config-v3", []domain.Scope{in.Scope, in.Scope}); err == nil {
		t.Fatal("duplicate scope accepted")
	}
}

func TestE4StartupRejectsIncompleteCloudShape(t *testing.T) {
	for _, damage := range []string{"DROP TABLE job_cloud_results", "DROP TABLE configured_scope_catalog", "ALTER TABLE job_cloud_results RENAME COLUMN raw_artifacts TO damaged_raw", "ALTER TABLE configured_scope_catalog RENAME COLUMN active TO damaged_active", "ALTER TABLE job_agent_results RENAME COLUMN body TO damaged_body"} {
		t.Run(damage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "incomplete.sqlite")
			s, err := Open(path, true)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.db.Exec(damage).Error; err != nil {
				t.Fatal(err)
			}
			s.Close()
			if stale, err := Open(path, false); err == nil {
				stale.Close()
				t.Fatal("startup accepted incomplete schema4")
			}
		})
	}
}
