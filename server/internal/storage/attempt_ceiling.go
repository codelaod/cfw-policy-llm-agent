package storage

import (
	"cfw-policy-llm-agent/server/internal/workload"
	"context"
)

type AttemptCeiling = workload.AttemptCeiling

func (s *Store) ReserveAttemptWithCeiling(ctx context.Context, jobID, class, source, revision string, ceiling AttemptCeiling) (string, error) {
	if e := ceiling.Validate(); e != nil {
		return "", ErrInputInvalid
	}
	return s.reserveAttempt(ctx, jobID, class, source, revision, &ceiling)
}
