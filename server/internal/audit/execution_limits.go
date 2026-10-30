package audit

import (
	"context"
	"time"
)

type ExecutionLimits struct {
	MaxAssets     int
	MaxPolicies   int
	MaxQueryCells int
	Timeout       time.Duration
}
type executionLimitKey struct{}

func LegacyExecutionLimits() ExecutionLimits {
	return ExecutionLimits{MaxAssets: 50, MaxPolicies: 1000, MaxQueryCells: MaxQueryCells, Timeout: 20 * time.Second}
}

// WithExecutionLimits binds one validated server-owned protection policy. Zero
// values are not replaced with defaults. Legacy callers alone use legacy limits.
func WithExecutionLimits(ctx context.Context, limits ExecutionLimits) (context.Context, error) {
	if limits.MaxAssets < 1 || limits.MaxPolicies < 1 || limits.MaxQueryCells < 1 || limits.Timeout <= 0 {
		return ctx, ErrInput
	}
	return context.WithValue(ctx, executionLimitKey{}, limits), nil
}
func executionLimits(ctx context.Context) ExecutionLimits {
	if v, ok := ctx.Value(executionLimitKey{}).(ExecutionLimits); ok {
		return v
	}
	return LegacyExecutionLimits()
}

type queryLimitKey struct{}

func WithQueryCellLimit(ctx context.Context, cells int) (context.Context, error) {
	if cells < 0 {
		return ctx, ErrInput
	}
	return context.WithValue(ctx, queryLimitKey{}, cells), nil
}
func queryCellLimit(ctx context.Context) int {
	if cells, ok := ctx.Value(queryLimitKey{}).(int); ok {
		return cells
	}
	return executionLimits(ctx).MaxQueryCells
}

type inputObjectLimitKey struct{}
type inputObjectLimits struct{ MaxAssets, MaxPolicies, ValidationCells int }

// WithInputObjectLimits controls validation sizes only; it does not enable a
// rules phase or refill a zero computation allowance in an assistant workload.
func WithInputObjectLimits(ctx context.Context, assets, policies int, validationCells ...int) (context.Context, error) {
	if assets < 1 || policies < 1 {
		return ctx, ErrInput
	}
	if len(validationCells) > 1 {
		return ctx, ErrInput
	}
	cells := 0
	if len(validationCells) == 1 {
		if validationCells[0] < 1 {
			return ctx, ErrInput
		}
		cells = validationCells[0]
	}
	return context.WithValue(ctx, inputObjectLimitKey{}, inputObjectLimits{assets, policies, cells}), nil
}
func validationObjectLimits(ctx context.Context) inputObjectLimits {
	if v, ok := ctx.Value(inputObjectLimitKey{}).(inputObjectLimits); ok {
		return v
	}
	l := executionLimits(ctx)
	return inputObjectLimits{l.MaxAssets, l.MaxPolicies, l.MaxQueryCells}
}

// Relational validation receives its own finite local protection. This context
// never escapes ValidateContext or grants tools a computation allowance.
func inputValidationContext(ctx context.Context) context.Context {
	cells := validationObjectLimits(ctx).ValidationCells
	if cells == 0 {
		cells = executionLimits(ctx).MaxQueryCells
	}
	child, _ := WithQueryCellLimit(ctx, cells)
	return child
}
