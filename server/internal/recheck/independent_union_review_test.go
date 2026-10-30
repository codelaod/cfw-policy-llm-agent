package recheck

import (
	"fmt"
	"math/rand"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

// Symbolic bounded flow sets independently check Compare's subset decision.
// These are algorithm fixtures, not labelled audit cases or real remediation.
func TestIndependentComparisonMatchesDiscreteFlowSetsAcrossActionLanes(t *testing.T) {
	rng := rand.New(rand.NewSource(911783))
	type point struct {
		address, port int
		action        string
	}
	makeSet := func() ([]domain.Cell, map[point]bool) {
		cells, points := []domain.Cell{}, map[point]bool{}
		for n := 1 + rng.Intn(8); n > 0; n-- {
			from, low := rng.Intn(8), 1+rng.Intn(8)
			to, high := from+rng.Intn(8-from), low+rng.Intn(9-low)
			action := []string{"accept", "drop"}[rng.Intn(2)]
			cells = append(cells, domain.Cell{AddressFrom: fmt.Sprintf("198.51.100.%d", from), AddressTo: fmt.Sprintf("198.51.100.%d", to), PortFrom: low, PortTo: high, Action: action})
			for address := from; address <= to; address++ {
				for port := low; port <= high; port++ {
					points[point{address, port, action}] = true
				}
			}
		}
		return cells, points
	}
	for trial := 0; trial < 400; trial++ {
		input, old := exactFlowPair()
		before, oldPoints := makeSet()
		after, newPoints := makeSet()
		old.Findings[0].Differences = before
		sibling := old.Findings[0]
		sibling.ID, sibling.RuleID, sibling.Verdict = "unchanged-inapplicable", "CHK-08", "not_applicable"
		sibling.QueryScope, sibling.Differences = domain.Query{}, nil
		old.Findings = append(old.Findings, sibling)
		next := copyResult(old)
		next.Findings[0].Differences = after
		want := "persistent"
		if len(newPoints) < len(oldPoints) {
			subset := true
			for point := range newPoints {
				if !oldPoints[point] {
					subset = false
					break
				}
			}
			if subset {
				want = "improved"
			}
		}
		comparison, err := Compare(input, old, input, next)
		if err != nil || !comparison.Comparable || len(comparison.Issues) != 1 || comparison.Issues[0].State != want {
			t.Fatalf("trial=%d discrete subset=%s old_points=%d new_points=%d result=%+v err=%v", trial, want, len(oldPoints), len(newPoints), comparison, err)
		}
	}
}
