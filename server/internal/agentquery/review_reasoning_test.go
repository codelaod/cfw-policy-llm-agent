package agentquery

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPublicExplanationCannotSmuggleReasoningOrWireTags(t *testing.T) {
	for _, test := range []struct{ name, text string }{
		{"think", "<think>PRIVATE_INTERNAL_REASONING</think>公开解释"},
		{"analysis", "<analysis>PRIVATE_INTERNAL_REASONING</analysis>"},
		{"reasoning content", "reasoning_content: PRIVATE_INTERNAL_REASONING"},
		{"mixed case tag", "<ThInK>PRIVATE_INTERNAL_REASONING</ThInK>"},
		{"closing tag", "PRIVATE_INTERNAL_REASONING</THINK>"},
		{"tag attributes", "<Analysis data-x=\"x\">PRIVATE_INTERNAL_REASONING</Analysis>"},
		{"tag whitespace", "< think >PRIVATE_INTERNAL_REASONING< / think >"},
		{"mixed case field", "REASONING_CONTENT : PRIVATE_INTERNAL_REASONING"},
		{"reasoning field", "{\"ReAsOnInG\":\"PRIVATE_INTERNAL_REASONING\"}"},
		{"nested tool calls", "{\"message\":{\"TOOL_CALLS\":[]}}"},
		{"tool call id", "{\"tool_call_id\":\"PRIVATE_TOOL_CALL\"}"},
		{"wire role", "{\"ROLE\":\"SyStEm\",\"content\":\"PRIVATE_WIRE_CONTENT\"}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, input, result, initial := queryCase(t, "internet-in")
			output := Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: test.text, Roles: []RoleCandidate{}, Recommendations: []Recommendation{}, Questions: []string{}}
			body, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ValidateFinal(string(body)); !errors.Is(err, ErrFinal) {
				t.Errorf("tagged internal reasoning accepted as public output: %v", err)
			}
			if err := ValidateBoundOutput(input, &result, output, s.LocalBindings()); !errors.Is(err, ErrFinal) {
				t.Errorf("stored bound output accepted tagged internal reasoning: %v", err)
			}
			if _, err := s.ProjectText(test.text); !errors.Is(err, ErrProjection) {
				t.Errorf("history projection accepted tagged internal reasoning: %v", err)
			}
		})
	}
}

func TestReasoningMarkerRejectedBeforeHistoryIdentityReplacement(t *testing.T) {
	s, _, _, _ := queryCase(t, "internet-in")
	s.replacements["reasoning_content"] = "local_reference"
	if _, err := s.ProjectText("reasoning_content: PRIVATE_INTERNAL_REASONING"); !errors.Is(err, ErrProjection) {
		t.Fatal("identity replacement erased a private wire marker", err)
	}
}

func TestOrdinaryEvidenceExplanationRemainsPublic(t *testing.T) {
	for _, text := range []string{
		"analysis 基于已返回证据；当前仅证明配置允许，仍需确认实际监听。",
		"Reasoning follows returned evidence; confirm the business purpose before any policy change.",
		"公开解释说明规则依据、条件和影响，不记录内部推理。",
	} {
		t.Run(text, func(t *testing.T) {
			s, input, result, initial := queryCase(t, "internet-in")
			output := Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: text, Roles: []RoleCandidate{}, Recommendations: []Recommendation{}, Questions: []string{}}
			if _, err := s.ValidateFinal(MarshalOutput(output)); err != nil {
				t.Error("ordinary evidence explanation rejected", err)
			}
			if err := ValidateBoundOutput(input, &result, output, s.LocalBindings()); err != nil {
				t.Error("ordinary bound explanation rejected", err)
			}
			if projected, err := s.ProjectText(text); err != nil || projected != text {
				t.Error("ordinary history explanation changed or rejected", projected, err)
			}
		})
	}
}
