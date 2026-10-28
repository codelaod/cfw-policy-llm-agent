package storage

import (
	"context"
	"encoding/json"
	"reflect"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

func validConfiguredScope(scope domain.Scope) bool {
	for _, value := range []string{scope.Ref, scope.AccountRef, scope.ControlPlaneRef, scope.PolicySetRef} {
		if !attemptAlias(value) {
			return false
		}
	}
	return scope.Provider == "aliyun" && scope.IPFamily == "ipv4" && (scope.Boundary == "internet" && enum(scope.Direction, "in", "out") && scope.GatewayRef == "" || scope.Boundary == "nat" && scope.Direction == "out" && attemptAlias(scope.GatewayRef))
}

func validateScopeIdentity(tx *gorm.DB, scope domain.Scope) error {
	var rows []struct{ Body string }
	if err := tx.Raw("SELECT json_extract(body,'$.scope') AS body FROM snapshots WHERE scope_ref=? UNION ALL SELECT body FROM configured_scope_catalog WHERE scope_ref=?", scope.Ref, scope.Ref).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var existing domain.Scope
		if json.Unmarshal([]byte(row.Body), &existing) != nil || existing != scope {
			return ErrConflict
		}
	}
	return nil
}

// RegisterConfiguredScopes updates the local scope directory only. It does not
// establish caller identity, grant effects, create a budget, or send requests.
func (s *Store) RegisterConfiguredScopes(ctx context.Context, sourceRef, revision string, scopes []domain.Scope) error {
	if !attemptAlias(sourceRef) || sourceRef == "local-offline" || !attemptAlias(revision) || len(scopes) > 100 {
		return ErrInputInvalid
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if !validConfiguredScope(scope) || seen[scope.Ref] {
			return ErrInputInvalid
		}
		seen[scope.Ref] = true
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, scope := range scopes {
			if err := validateScopeIdentity(tx, scope); err != nil {
				return err
			}
			var source string
			if err := tx.Raw("SELECT source_ref FROM configured_scope_catalog WHERE scope_ref=?", scope.Ref).Scan(&source).Error; err != nil {
				return err
			}
			if source != "" && source != sourceRef {
				return ErrConflict
			}
		}
		if err := tx.Exec("UPDATE configured_scope_catalog SET active=0 WHERE source_ref=?", sourceRef).Error; err != nil {
			return err
		}
		for _, scope := range scopes {
			body, err := marshalBounded(scope, 4096)
			if err != nil {
				return err
			}
			if err = tx.Exec("INSERT INTO configured_scope_catalog(scope_ref,source_ref,config_revision,body,active) VALUES(?,?,?,?,1) ON CONFLICT(scope_ref) DO UPDATE SET config_revision=excluded.config_revision,active=1", scope.Ref, sourceRef, revision, string(body)).Error; err != nil {
				return err
			}
		}
		return ctx.Err()
	})
}

func validCloudSelection(scope domain.Scope, selection domain.Selection, check bool) bool {
	if check && selection.Kind == "" && len(selection.AssetIDs) == 0 {
		return true
	}
	if selection.Kind == "gateway_policy" {
		return scope.Boundary == "nat" && len(selection.AssetIDs) == 0
	}
	if selection.Kind != "assets" || len(selection.AssetIDs) < 1 || len(selection.AssetIDs) > 50 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range selection.AssetIDs {
		if !attemptAlias(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func validateCloudPlan(tx *gorm.DB, p ExecutionPlan) error {
	if p.SnapshotID != "" || len(p.Heads) != 0 || p.ContextReportID != "" || !p.EffectCeiling.AllowCloudReads || p.EffectCeiling.CloudHTTPAttempts < 1 || p.EffectCeiling.AllowModelRequests || p.EffectCeiling.ModelHTTPAttempts != 0 || p.PreparedInput.Scope.Ref != p.PolicyScopeRef || !validConfiguredScope(p.PreparedInput.Scope) || !validCloudSelection(p.PreparedInput.Scope, p.PreparedInput.Selection, p.Kind == "connection_check") {
		return ErrInputInvalid
	}
	var row struct {
		SourceRef, ConfigRevision, Body string
		Active                          bool
	}
	r := tx.Raw("SELECT source_ref,config_revision,body,active FROM configured_scope_catalog WHERE scope_ref=?", p.PolicyScopeRef).Scan(&row)
	if r.Error != nil {
		return r.Error
	}
	var scope domain.Scope
	if r.RowsAffected != 1 || !row.Active || row.SourceRef != p.DataSourceID || row.ConfigRevision != p.ConfigRevision || json.Unmarshal([]byte(row.Body), &scope) != nil || scope != p.PreparedInput.Scope {
		return ErrInputInvalid
	}
	intent := domain.Input{Scope: p.PreparedInput.Scope, Selection: p.PreparedInput.Selection, Baseline: p.PreparedInput.Baseline}
	if !reflect.DeepEqual(intent, p.PreparedInput) {
		return ErrInputInvalid
	}
	return nil
}
