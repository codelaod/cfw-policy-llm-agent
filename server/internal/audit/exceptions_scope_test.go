package audit

import (
	"context"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestNATExceptionApprovesOnlyItsExplicitSubcheck(t *testing.T) {
	in, err := SyntheticExample("nat-out")
	if err != nil {
		t.Fatal(err)
	}
	in.Baseline.Exceptions = []domain.Exception{{CheckID: "NAT-02", Subcheck: "OUT-01", SubjectRef: in.Assets[0].ID, ServiceRef: in.Assets[0].Services[0].ID, ScopeRef: in.Scope.Ref, ExpiresAt: "2026-10-29T00:00:00Z", Reason: "人工限定目标上界例外", Confirmed: true}}
	result, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	upper, required := false, false
	for _, finding := range result.Findings {
		if finding.RuleID != "NAT-02" {
			continue
		}
		if finding.Subcheck == "OUT-01" {
			upper = true
			if !finding.AcceptedException || finding.Verdict != "fail" {
				t.Fatal("explicit exception lost original fact or was not bound")
			}
		}
		if finding.Subcheck == "OUT-02" {
			required = true
			if finding.AcceptedException {
				t.Fatal("upper-bound exception silently approved required-access failure")
			}
		}
	}
	if !upper || !required {
		t.Fatal("example must exercise both independent NAT checks")
	}
	in.Baseline.Exceptions[0].CheckID = "CHK-07"
	if Validate(in) == nil {
		t.Fatal("NAT subcheck cannot be attached to an unrelated check")
	}
}
