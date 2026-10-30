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

func TestE4SchemaUpgradeIsExplicitAndPreservesFrozenInputAndBudget(t *testing.T) {
	for _, version := range []string{"2", "3"} {
		t.Run(version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "upgrade.sqlite")
			s, err := Open(path, true)
			if err != nil {
				t.Fatal(err)
			}
			in, _ := audit.SyntheticExample("internet-in")
			saved, err := s.SaveSnapshot(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ConfigureBudgetPolicy(context.Background(), BudgetPolicy{Ref: "old-explicit-budget", ApplicationCloudAttempts: 7, SessionCloudAttempts: 4, ExpiresAt: "2099-01-01T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{"DROP TABLE job_cloud_results", "DROP TABLE configured_scope_catalog", "DROP TABLE business_schema", "CREATE TABLE business_schema(version INTEGER PRIMARY KEY CHECK(version=" + version + "))", "INSERT INTO business_schema VALUES(" + version + ")"} {
				if err = s.db.Exec(q).Error; err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			if stale, err := Open(path, false); err == nil {
				stale.Close()
				t.Fatal("startup migrated old database")
			}
			s, err = Open(path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var current int
			if err = s.db.Raw("SELECT version FROM business_schema").Scan(&current).Error; err != nil || current != BusinessSchemaVersion {
				t.Fatal("schema4 unavailable", current, err)
			}
			loaded, err := s.GetSnapshot(context.Background(), saved.ID)
			if err != nil || loaded.Body != saved.Body {
				t.Fatal("migration changed frozen input", err)
			}
			policy, err := s.GetBudgetPolicy(context.Background(), "old-explicit-budget")
			if err != nil || policy.ApplicationCloudAttempts != 7 || policy.SessionCloudAttempts != 4 {
				t.Fatal("migration changed budget", err)
			}
		})
	}
}

func TestE4StartupRejectsIncompleteCloudShape(t *testing.T) {
	for _, damage := range []string{"DROP TABLE job_cloud_results", "DROP TABLE configured_scope_catalog", "ALTER TABLE job_cloud_results RENAME COLUMN raw_artifacts TO damaged_raw", "ALTER TABLE configured_scope_catalog RENAME COLUMN active TO damaged_active", "ALTER TABLE job_agent_results RENAME COLUMN body TO damaged_body", "ALTER TABLE budget_policies RENAME COLUMN expires_at TO damaged_expiry"} {
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

func TestE4SchemaUpgradeRetainsReservedUnknownAttempts(t *testing.T) {
	s, initial, job := e3BudgetCase(t, 5, 5, 5, 5)
	if _, err := s.ReserveAttempt(context.Background(), job.ID, "model", "configured-model", initial.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	var file struct{ File string }
	if err := s.db.Raw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&file).Error; err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP TABLE job_cloud_results", "DROP TABLE configured_scope_catalog", "DROP TABLE business_schema", "CREATE TABLE business_schema(version INTEGER PRIMARY KEY CHECK(version=3))", "INSERT INTO business_schema VALUES(3)"} {
		if err := s.db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	reopened, err := Open(file.File, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	usage, err := reopened.BudgetUsage(context.Background(), initial.BudgetRef, "", job.ID)
	if err != nil || usage.ModelAttempts != 1 || usage.UnknownDelivery != 1 || usage.UnknownUsageAttempts != 1 || usage.ReportedTokens != nil {
		t.Fatal("schema4 migration refunded or filled unknown attempt", usage, err)
	}
}
