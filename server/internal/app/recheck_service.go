package app

import (
	"context"
	"encoding/json"

	"cfw-policy-llm-agent/server/internal/recheck"
	"cfw-policy-llm-agent/server/internal/storage"
)

// CompareRuns links immutable audits. It performs no refresh, execution or
// cloud write; a new audit is an independently accepted operation.
func (s *AuditService) CompareRuns(ctx context.Context, oldID, newID string) (storage.RecheckRecord, error) {
	oldRun, err := s.Store.GetAudit(ctx, oldID)
	if err != nil {
		return storage.RecheckRecord{}, err
	}
	newRun, err := s.Store.GetAudit(ctx, newID)
	if err != nil {
		return storage.RecheckRecord{}, err
	}
	comparison, err := recheck.Compare(oldRun.Input, oldRun.Result, newRun.Input, newRun.Result)
	if err != nil {
		return storage.RecheckRecord{}, err
	}
	encoded, err := json.Marshal(comparison)
	if err != nil {
		return storage.RecheckRecord{}, err
	}
	return s.Store.SaveRecheck(ctx, oldID, newID, encoded)
}
