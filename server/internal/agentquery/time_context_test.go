package agentquery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
)

func TestQueryContextKeepsFrozenEvaluationAndCollectionTimes(t *testing.T) {
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	in.SourceKind = "imported"
	in.EvaluationAt = "2025-01-02T08:03:04.123+08:00"
	in.CollectedAt = "2024-12-31T23:04:05-05:00"
	s, e := New(in, nil)
	if e != nil {
		t.Fatal(e)
	}
	var initial map[string]any
	if e = json.Unmarshal([]byte(s.InitialContext()), &initial); e != nil {
		t.Fatal(e)
	}
	scope := initial["scope_ref"].(string)
	view := invoke(t, s, "get_partition_context", "scope_ref", scope)
	partition := data(view)
	for _, field := range []struct{ name, want string }{{"evaluation_at", in.EvaluationAt}, {"collected_at", in.CollectedAt}} {
		want, _ := time.Parse(time.RFC3339Nano, field.want)
		for _, object := range []map[string]any{initial, partition} {
			raw, ok := object[field.name].(string)
			if !ok {
				t.Fatal("frozen time absent", field.name)
			}
			got, e := time.Parse(time.RFC3339Nano, raw)
			if e != nil || !got.Equal(want) {
				t.Fatal("frozen time replaced by wall clock", field.name, raw)
			}
		}
	}
	in.EvaluationAt = "2035-01-01T00:00:00Z"
	in.CollectedAt = "2034-01-01T00:00:00Z"
	again := invoke(t, s, "get_partition_context", "scope_ref", scope)
	if data(again)["evaluation_at"] != partition["evaluation_at"] || data(again)["collected_at"] != partition["collected_at"] {
		t.Fatal("caller mutated fixed clock projection")
	}
	if initial["source_kind"] != "imported" || view["assessment_basis"] != "imported_assumption" {
		t.Fatal("time disclosure upgraded source")
	}
}
func TestPartitionConsistencyIsControlledAndPrivateGapsStayHidden(t *testing.T) {
	for _, value := range []string{"fixed_artifact", "observed_stable", "changed", "unverified", "unknown", "PRIVATE_CONSISTENCY https://private.example.invalid/secret"} {
		t.Run(value, func(t *testing.T) {
			in, e := audit.SyntheticExample("internet-in")
			if e != nil {
				t.Fatal(e)
			}
			in.Completeness.ObservationConsistency = value
			in.Semantics.ProfileID = "synthetic-v1"
			in.Semantics.SourceRef = "https://private.example.invalid/provider-source"
			in.Completeness.KnownGaps = []string{"PRIVATE_GAP_DESCRIPTION"}
			s, e := New(in, nil)
			if e != nil {
				t.Fatal(e)
			}
			var initial InitialView
			json.Unmarshal([]byte(s.InitialContext()), &initial)
			args, _ := json.Marshal(map[string]string{"scope_ref": initial.ScopeRef})
			raw, e := s.Invoke(context.Background(), "get_partition_context", string(args))
			if e != nil {
				t.Fatal(e)
			}
			var out map[string]any
			json.Unmarshal([]byte(raw), &out)
			want := value
			if strings.HasPrefix(value, "PRIVATE_") {
				want = "unknown"
			}
			if data(out)["observation_consistency"] != want {
				t.Fatal("consistency missing or unsafe", data(out)["observation_consistency"])
			}
			if strings.Contains(raw, "PRIVATE_") || strings.Contains(raw, "private.example.invalid") {
				t.Fatal("private consistency/gap text escaped projection")
			}
		})
	}
}
func TestFixedRFC3339TimesAreNotMistakenForLiteralIPv6(t *testing.T) {
	s, _, _, initial := queryCase(t, "internet-in")
	text := "本次固定评估时点为 2026-10-28T01:00:00Z，资料时点为 2026-10-28T08:00:00+08:00。"
	if _, e := s.ProjectText(text); e != nil {
		t.Fatal("safe fixed timestamp was mistaken for a network address", e)
	}
	out := Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: text}
	if _, e := s.ValidateFinal(MarshalOutput(out)); e != nil {
		t.Fatal("fixed-time explanation rejected", e)
	}
	out.Explanation = text + " 同时添加 2001:db8::1"
	if _, e := s.ValidateFinal(MarshalOutput(out)); e == nil {
		t.Fatal("IPv6 address escaped beside a valid timestamp")
	}
}
func TestUnknownConsistencyTextCannotReenterThroughPublicHistory(t *testing.T) {
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	in.Completeness.ObservationConsistency = "PRIVATE_UNSUPPORTED_CONSISTENCY"
	s, e := New(in, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ProjectText("原始声明为 PRIVATE_UNSUPPORTED_CONSISTENCY"); e == nil {
		t.Fatal("unsupported observation text reentered through history projection")
	}
}
