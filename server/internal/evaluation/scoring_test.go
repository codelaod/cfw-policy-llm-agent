package evaluation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/agentrun"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/workload"
)

func scoringFixture(t *testing.T) (Protocol, FrozenCase, LabelSet) {
	t.Helper()
	profile, e := workload.Normalize("audit", workload.Resolved{})
	if e != nil {
		t.Fatal(e)
	}
	input, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	d := CaseDefinition{ID: "case-one", ScenarioRef: "scenario-one", Split: "development", Repetitions: 1, LabelVersionRef: "test-labels", LabelReviewState: "proposed"}
	c, e := FreezeCase(context.Background(), d, input, profile)
	if e != nil {
		t.Fatal(e)
	}
	p := Protocol{SchemaVersion: 1, Version: "score-fixture", DatasetUse: "development", Cases: []CaseDefinition{d}, CommonBriefVersion: "brief-v1", CommonBrief: "人工测试共同任务。", ModelConfigRef: "same-test-model", Sampling: Sampling{Temperature: 0.2, TopP: 1}, Workload: profile, ToolProjectionVersion: ToolProjectionVersion, FinalSchemaVersion: FinalSchemaVersion, AgentPromptVersion: AgentPromptVersion, FixedPromptVersion: FixedPromptVersion, FixedPipeline: agentrun.FixedPipeline{Version: "fixed-v1", Steps: []agentrun.FixedStep{{Tool: "get_asset_context", Selector: "assets", MaxItems: 1}}}}
	h, e := protocolDigest(p)
	if e != nil {
		t.Fatal(e)
	}
	labels := LabelSet{Version: "test-labels", ProtocolDigest: h, ReviewState: "proposed"}
	checks, keys := map[string]bool{}, map[FactKey]bool{}
	for _, finding := range c.rules.Findings {
		key := checkKey(finding.RuleID, finding.Subcheck)
		if !checks[key] {
			checks[key] = true
			labels.CheckFamilies = append(labels.CheckFamilies, CheckFamily{RuleID: finding.RuleID, Subcheck: finding.Subcheck, Family: "combined-configuration"})
		}
		fact := FactKey{CaseID: c.Definition.ID, SubjectRef: finding.SubjectRef, ServiceRef: finding.ServiceRef, Family: "combined-configuration"}
		if !keys[fact] {
			keys[fact] = true
			labels.Facts = append(labels.Facts, FactLabel{Key: fact, Expected: "negative"})
		}
	}
	// These deliberately contrary labels exercise the scorer contract. They
	// are test data, never frozen human ground truth or evaluation results.
	return p, c, labels
}
func TestRuleScorerCollapsesFamiliesAndKeepsZeroDenominatorUnknownAndUnlabelled(t *testing.T) {
	p, c, labels := scoringFixture(t)
	labels.Facts = append(labels.Facts, FactLabel{Key: FactKey{CaseID: c.Definition.ID, SubjectRef: "PRIVATE_LABEL_SENTINEL", Family: "pending-business"}, Expected: "insufficient_data"})
	scorer, e := NewScorer(p, labels)
	if e != nil {
		t.Fatal(e)
	}
	score, e := scorer.ScoreRules([]FrozenCase{c})
	if e != nil || score.FP == 0 || score.GroupedProblems >= score.RawFindings || score.Precision.Value == nil || *score.Precision.Value != 0 || score.Recall.Status != "not_applicable" || score.Recall.Value != nil || score.NonDeterminateLabels["insufficient_data"] != 1 || score.NonDeterminateOutputs["absent"] != 1 {
		t.Fatal(score, e)
	}
	for i := range labels.Facts {
		labels.Facts[i].Expected = "needs_confirmation"
	}
	scorer, e = NewScorer(p, labels)
	if e != nil {
		t.Fatal(e)
	}
	score, e = scorer.ScoreRules([]FrozenCase{c})
	if e != nil || score.TP != 0 || score.FP != 0 || score.FN != 0 || score.Precision.Value != nil || score.Recall.Value != nil || score.F1.Value != nil {
		t.Fatal("undetermined labels inflated accuracy", score, e)
	}
	labels.Facts = labels.Facts[len(labels.Facts)-1:]
	scorer, e = NewScorer(p, labels)
	if e != nil {
		t.Fatal(e)
	}
	score, e = scorer.ScoreRules([]FrozenCase{c})
	if e != nil || score.UnlabelledOutputs != score.GroupedProblems {
		t.Fatal("unlabelled findings disappeared", score, e)
	}
}
func TestRuleScorerRequiresFrozenMappingAndCountsAbsentKnownPositiveAsMiss(t *testing.T) {
	p, c, labels := scoringFixture(t)
	labels.Facts = append(labels.Facts, FactLabel{Key: FactKey{CaseID: c.Definition.ID, SubjectRef: "test-positive-subject", Family: "expected-risk"}, Expected: "positive"})
	scorer, e := NewScorer(p, labels)
	if e != nil {
		t.Fatal(e)
	}
	score, e := scorer.ScoreRules([]FrozenCase{c})
	if e != nil || score.FN != 1 || score.AbstainedPositive != 1 || score.Recall.Value == nil || *score.Recall.Value != 0 {
		t.Fatal(score, e)
	}
	labels.CheckFamilies = labels.CheckFamilies[:len(labels.CheckFamilies)-1]
	scorer, e = NewScorer(p, labels)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = scorer.ScoreRules([]FrozenCase{c}); !errors.Is(e, ErrScoring) {
		t.Fatal("unmapped rule silently omitted", e)
	}
}
func TestModelValidationNeverBecomesQualityScoreWithoutIndependentJudgment(t *testing.T) {
	out := RunOutcome{Record: RunRecord{RunID: "model-run", ModelOutputRef: "output-one", ModelLayer: "produced", ModelOutputAbsent: false}, ModelOutput: &agentquery.Output{Recommendations: []agentquery.Recommendation{{Kind: "confirm_information"}, {Kind: "policy_change"}}}}
	if score, e := ScoreModel(out, nil); e != nil || score.Status != "pending_independent_review" {
		t.Fatal(score, e)
	}
	review := &ModelReview{RunID: "model-run", ModelOutputRef: "output-one", Version: "human-review-v1", ReviewRef: "reviewer-local-v1", InferenceComplete: true, Inferences: []InferenceJudgment{{AssertionRef: "wrong-claim", Kind: "assertion", Verdict: "incorrect"}, {AssertionRef: "kept-question", Kind: "kept_unconfirmed", Verdict: "correct"}}, Recommendations: []RecommendationJudgment{{Index: 0, ObjectCorrect: "satisfied", EvidenceSupported: "satisfied", BusinessConstraints: "unknown", NoUnauthorizedOpening: "satisfied"}, {Index: 1, ObjectCorrect: "satisfied", EvidenceSupported: "satisfied", BusinessConstraints: "satisfied", NoUnauthorizedOpening: "not_satisfied"}}}
	score, e := ScoreModel(out, review)
	if e != nil || score.Status != "independently_reviewed" || score.RecommendationCounts["unknown"] != 1 || score.RecommendationCounts["not_satisfied"] != 1 || score.RecommendationCounts["satisfied"] != 0 || score.InferenceCounts["assertion_incorrect"] != 1 || score.InferenceCounts["kept_unconfirmed_correct"] != 1 {
		t.Fatal(score, e)
	}
	review.Recommendations = review.Recommendations[:1]
	if _, e = ScoreModel(out, review); !errors.Is(e, ErrScoring) {
		t.Fatal("missing recommendation review accepted", e)
	}
	out.Record.ModelOutputAbsent = true
	if _, e = ScoreModel(out, review); !errors.Is(e, ErrScoring) {
		t.Fatal("absent model got quality score", e)
	}
}
func TestFormalProtocolDoesNotInventHumanReviewedCases(t *testing.T) {
	p, _, labels := scoringFixture(t)
	p.DatasetUse = "formal"
	if p.Validate() == nil {
		t.Fatal("one development case counted as formal evaluation")
	}
	p.DatasetUse = "development"
	labels.ProtocolDigest = strings.Repeat("0", 64)
	if _, e := NewScorer(p, labels); !errors.Is(e, ErrScoring) {
		t.Fatal("labels for another protocol accepted", e)
	}
}
