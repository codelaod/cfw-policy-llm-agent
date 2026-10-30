package storage

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"reflect"
)

// executionPlanForJob returns an execution view. The accepted request and its
// stored plan remain immutable; only a committed, sealed binding can supply the
// late-resolved input and snapshot to downstream stages.
func executionPlanForJob(tx *gorm.DB, job Job) (ExecutionPlan, error) {
	p, err := planRecord(tx, job.PlanID)
	if err != nil {
		return p, err
	}
	if p.ConsumedJobID != job.ID || p.ConversationID != job.ConversationID || p.Kind != job.Kind {
		return p, ErrInputInvalid
	}
	if p.Preparation == nil {
		return p, nil
	}
	b, err := preparedBinding(tx, job.ID)
	if errors.Is(err, ErrNotFound) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	input, err := jobInput(tx, job.ID)
	if err != nil || b.JobID != job.ID || b.Scope != input.Scope || !reflect.DeepEqual(b.Selection, input.Selection) || !reflect.DeepEqual(b.Input, input) || !reflect.DeepEqual(b.Manifest, manifestFor(input)) {
		return p, ErrInputInvalid
	}
	p.SnapshotID, p.PolicyScopeRef, p.PreparedInput, p.Heads = b.SnapshotID, b.Scope.Ref, b.Input, b.Heads
	return p, nil
}

func (s *Store) ExecutionPlanForJob(ctx context.Context, id string) (plan ExecutionPlan, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, id)
		if e != nil {
			return e
		}
		plan, e = executionPlanForJob(tx, job)
		return e
	})
	return
}
