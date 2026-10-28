package audit

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

func largeAllowedFixture() domain.Input {
	in := fixture()
	in.Baseline.EnabledChecks = []string{"CHK-03"}
	in.Semantics.DefaultAction = "accept"
	s := &in.Assets[0].Services[0]
	s.Ports = []domain.PortRange{{From: 1, To: 200}}
	s.AllowedConfirmed = true
	s.AllowedComplete = true
	for i := 0; i < 1000; i++ {
		n := i % 100
		s.Allowed = append(s.Allowed, domain.Access{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"198.51.100." + strconv.Itoa(2*n)}, Ports: []domain.PortRange{{From: 2*n + 1, To: 2*n + 1}}, Applicability: "applicable"})
	}
	return in
}

func TestLargeUpperBoundCooperatesWithCancellation(t *testing.T) {
	in := largeAllowedFixture()
	if err := Validate(in); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	result, err := Run(ctx, in)
	elapsed := time.Since(started)
	t.Logf("elapsed=%s, error=%v, evaluated_cells=%d", elapsed, err, result.Coverage.EvaluatedCells)
	if elapsed > time.Second {
		t.Fatalf("cancelled bounded calculation kept running for %s", elapsed)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err != nil && result.Coverage.Complete {
		t.Fatal("cancelled calculation reported complete coverage")
	}
}

func TestValidationEntryHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ValidateContext(ctx, largeAllowedFixture()); !errors.Is(err, context.Canceled) {
		t.Fatal("validation ignored already cancelled parent context")
	}
	if _, err := DecodeImportContext(ctx, []byte("invalid")); !errors.Is(err, context.Canceled) {
		t.Fatal("import continued parsing after cancellation")
	}
}

type cancellingContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancellingContext) Err() error {
	c.checks++
	if c.checks == 7 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestInterruptedDifferenceRetainsAlreadyProvedCells(t *testing.T) {
	in := largeAllowedFixture()
	allowed, err := compileAccess(context.Background(), in.Assets[0].Services[0].Allowed)
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancellingContext{Context: parent, cancel: cancel}
	cell := domain.Cell{AddressFrom: "0.0.0.0", AddressTo: "255.255.255.255", PortFrom: 1, PortTo: 200, Action: "accept", Broad: true}
	partial, complete, err := outsideAccess(ctx, cell, allowed, "tcp")
	if !errors.Is(err, context.Canceled) || complete || len(partial) == 0 {
		t.Fatalf("cancellation lost proven cells or claimed completeness: partial=%d complete=%v error=%v", len(partial), complete, err)
	}
}
