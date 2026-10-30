package storage

import (
	"context"
	"gorm.io/gorm"
)

// BudgetForOwner reads a previously bound authorization. It never creates a
// policy, resets attempts, changes a head or follows a machine credential profile.
func (s *Store) BudgetForOwner(ctx context.Context, owner string) (policy BudgetPolicy, err error) {
	if !attemptAlias(owner) {
		return policy, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ref string
		r := tx.Raw("SELECT budget_ref FROM execution_budget_bindings WHERE owner_ref=?", owner).Scan(&ref)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrBudgetNotConfigured
		}
		var e error
		head, e := budgetHead(tx, ref)
		if e != nil {
			return e
		}
		policy = head.Policy
		return nil
	})
	return
}
