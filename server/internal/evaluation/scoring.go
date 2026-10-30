package evaluation

import (
	"errors"
	"sort"
)

var ErrScoring = errors.New("EVALUATION_SCORING_INVALID")

// Labels are supplied only to this independent scorer. Runner, FrozenCase,
// model factories and QueryService do not accept this type.
type LabelSet struct {
	Version        string        `json:"label_set_version"`
	ProtocolDigest string        `json:"protocol_digest"`
	ReviewState    string        `json:"review_state"`
	ReviewRef      string        `json:"review_ref,omitempty"`
	CheckFamilies  []CheckFamily `json:"check_families"`
	Facts          []FactLabel   `json:"facts"`
}
type CheckFamily struct {
	RuleID   string `json:"rule_id"`
	Subcheck string `json:"subcheck,omitempty"`
	Family   string `json:"problem_family"`
}
type FactKey struct {
	CaseID     string `json:"case_id"`
	SubjectRef string `json:"subject_ref"`
	ServiceRef string `json:"service_ref,omitempty"`
	Family     string `json:"problem_family"`
}
type FactLabel struct {
	Key      FactKey `json:"key"`
	Expected string  `json:"expected"`
}
type Ratio struct {
	Status      string   `json:"status"`
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Value       *float64 `json:"value"`
}
type RuleScore struct {
	LabelVersion          string         `json:"label_version"`
	ReviewState           string         `json:"label_review_state"`
	ReviewRef             string         `json:"label_review_ref,omitempty"`
	TP                    int            `json:"tp"`
	FP                    int            `json:"fp"`
	FN                    int            `json:"fn"`
	TN                    int            `json:"tn"`
	AbstainedPositive     int            `json:"abstained_positive"`
	AbstainedNegative     int            `json:"abstained_negative"`
	NonDeterminateLabels  map[string]int `json:"non_determinate_labels"`
	NonDeterminateOutputs map[string]int `json:"non_determinate_outputs"`
	UnlabelledOutputs     int            `json:"unlabelled_outputs"`
	RawFindings           int            `json:"raw_findings"`
	GroupedProblems       int            `json:"grouped_problems"`
	Precision             Ratio          `json:"precision"`
	Recall                Ratio          `json:"recall"`
	F1                    Ratio          `json:"f1"`
	Qualification         string         `json:"qualification"`
}
type Scorer struct {
	protocol Protocol
	labels   LabelSet
	families map[string]string
}

func NewScorer(p Protocol, labels LabelSet) (*Scorer, error) {
	hash, e := protocolDigest(p)
	if e != nil || labels.ProtocolDigest != hash || !publicRef.MatchString(labels.Version) || len(labels.CheckFamilies) == 0 || len(labels.Facts) == 0 || labels.ReviewState != "proposed" && labels.ReviewState != "human_reviewed" || labels.ReviewState == "human_reviewed" && !publicRef.MatchString(labels.ReviewRef) || p.DatasetUse == "formal" && labels.ReviewState != "human_reviewed" {
		return nil, ErrScoring
	}
	caseIDs := map[string]bool{}
	for _, c := range p.Cases {
		caseIDs[c.ID] = true
		if c.LabelVersionRef != labels.Version {
			return nil, ErrScoring
		}
	}
	families := map[string]string{}
	for _, entry := range labels.CheckFamilies {
		key := checkKey(entry.RuleID, entry.Subcheck)
		if !publicRef.MatchString(entry.RuleID) || !publicRef.MatchString(entry.Family) || entry.Subcheck != "" && !publicRef.MatchString(entry.Subcheck) || families[key] != "" {
			return nil, ErrScoring
		}
		families[key] = entry.Family
	}
	keys := map[FactKey]bool{}
	labelledCases := map[string]bool{}
	for _, label := range labels.Facts {
		key := label.Key
		if !caseIDs[key.CaseID] || key.SubjectRef == "" || len(key.SubjectRef) > 256 || len(key.ServiceRef) > 256 || !publicRef.MatchString(key.Family) || keys[key] || !labelVerdict(label.Expected) {
			return nil, ErrScoring
		}
		keys[key] = true
		labelledCases[key.CaseID] = true
	}
	if p.DatasetUse == "formal" {
		for id := range caseIDs {
			if !labelledCases[id] {
				return nil, ErrScoring
			}
		}
	}
	detached, e := copyJSON(labels)
	if e != nil {
		return nil, ErrScoring
	}
	frozenProtocol, e := copyJSON(p)
	if e != nil {
		return nil, ErrScoring
	}
	return &Scorer{protocol: frozenProtocol, labels: detached, families: families}, nil
}
func checkKey(ruleID, subcheck string) string { return ruleID + "\x00" + subcheck }
func labelVerdict(v string) bool {
	return v == "positive" || v == "negative" || v == "needs_confirmation" || v == "insufficient_data" || v == "unsupported" || v == "not_applicable" || v == "unknown"
}
func ratio(n, d int) Ratio {
	result := Ratio{Status: "not_applicable", Numerator: n, Denominator: d}
	if d > 0 {
		value := float64(n) / float64(d)
		result.Status, result.Value = "computed", &value
	}
	return result
}

// ScoreRules scores the shared frozen deterministic layer once. Its result is
// common to all arms. Run availability/failure is counted separately by
// Aggregate; an absent model output can never improve this score.
func (s *Scorer) ScoreRules(cases []FrozenCase) (RuleScore, error) {
	if s == nil || len(cases) != len(s.protocol.Cases) {
		return RuleScore{}, ErrScoring
	}
	groups := map[FactKey]map[string]bool{}
	seenCases := map[string]bool{}
	score := RuleScore{LabelVersion: s.labels.Version, ReviewState: s.labels.ReviewState, ReviewRef: s.labels.ReviewRef, NonDeterminateLabels: map[string]int{}, NonDeterminateOutputs: map[string]int{}, Qualification: "Common frozen rule layer only; identical rule inputs imply identical arm metrics. Missing run outputs and model-added errors are reported separately. Format validation does not establish recommendation quality."}
	for _, c := range cases {
		if seenCases[c.Definition.ID] || !validAssignment(s.protocol, c, Assignment{RunID: "score", CaseID: c.Definition.ID, JobID: "score", Arm: RulesOnly, Repetition: 1}) || !equalJSON(c.RuleWorkload, s.protocol.Workload) {
			return RuleScore{}, ErrScoring
		}
		seenCases[c.Definition.ID] = true
		h, _ := digest(c.rules)
		if h != c.CommonRuleDigest {
			return RuleScore{}, ErrScoring
		}
		for _, finding := range c.rules.Findings {
			family := s.families[checkKey(finding.RuleID, finding.Subcheck)]
			if family == "" {
				return RuleScore{}, ErrScoring
			}
			key := FactKey{CaseID: c.Definition.ID, SubjectRef: finding.SubjectRef, ServiceRef: finding.ServiceRef, Family: family}
			if groups[key] == nil {
				groups[key] = map[string]bool{}
			}
			groups[key][finding.Verdict] = true
			score.RawFindings++
		}
	}
	score.GroupedProblems = len(groups)
	for _, label := range s.labels.Facts {
		observed, exists := groups[label.Key]
		delete(groups, label.Key)
		classification := "absent"
		if exists {
			classification = groupVerdict(observed)
		}
		if classification != "positive" && classification != "negative" {
			score.NonDeterminateOutputs[classification]++
		}
		switch label.Expected {
		case "positive":
			if classification == "positive" {
				score.TP++
			} else {
				score.FN++
				if classification != "negative" {
					score.AbstainedPositive++
				}
			}
		case "negative":
			if classification == "positive" {
				score.FP++
			} else if classification == "negative" {
				score.TN++
			} else {
				score.AbstainedNegative++
			}
		default:
			score.NonDeterminateLabels[label.Expected]++
		}
	}
	score.UnlabelledOutputs = len(groups)
	score.Precision, score.Recall, score.F1 = ratio(score.TP, score.TP+score.FP), ratio(score.TP, score.TP+score.FN), ratio(2*score.TP, 2*score.TP+score.FP+score.FN)
	return score, nil
}
func groupVerdict(values map[string]bool) string {
	if values["fail"] {
		return "positive"
	}
	// A pass does not erase a related undecidable check in the same family.
	for _, v := range []string{"insufficient_data", "needs_confirmation", "unsupported"} {
		if values[v] {
			return v
		}
	}
	if values["pass"] {
		return "negative"
	}
	if values["not_applicable"] {
		return "not_applicable"
	}
	return "unknown"
}

type RecommendationJudgment struct {
	Index                 int    `json:"recommendation_index"`
	ObjectCorrect         string `json:"object_correct"`
	EvidenceSupported     string `json:"evidence_supported"`
	BusinessConstraints   string `json:"business_constraints_satisfied"`
	NoUnauthorizedOpening string `json:"no_unauthorized_opening"`
}
type InferenceJudgment struct {
	AssertionRef string `json:"assertion_ref"`
	Kind         string `json:"kind"`
	Verdict      string `json:"verdict"`
}
type ModelReview struct {
	RunID             string                   `json:"run_id"`
	ModelOutputRef    string                   `json:"model_output_ref"`
	Version           string                   `json:"review_version"`
	ReviewRef         string                   `json:"independent_review_ref"`
	InferenceComplete bool                     `json:"inference_review_complete"`
	Inferences        []InferenceJudgment      `json:"inferences"`
	Recommendations   []RecommendationJudgment `json:"recommendations"`
}
type ModelScore struct {
	Status               string                    `json:"status"`
	ReviewVersion        string                    `json:"review_version,omitempty"`
	ReviewRef            string                    `json:"independent_review_ref,omitempty"`
	InferenceReview      string                    `json:"inference_review"`
	RecommendationReview string                    `json:"recommendation_review"`
	InferenceCounts      map[string]int            `json:"inference_counts"`
	RecommendationCounts map[string]int            `json:"recommendation_counts"`
	CriterionCounts      map[string]map[string]int `json:"criterion_counts"`
}

func criterion(v string) bool { return v == "satisfied" || v == "not_satisfied" || v == "unknown" }

// ScoreModel requires explicit independent judgments of every recommendation.
// It does not infer correctness from model confidence, schema validity, a
// rule's score, local adoption, or an unexecuted cloud remediation.
func ScoreModel(out RunOutcome, review *ModelReview) (ModelScore, error) {
	score := ModelScore{Status: "not_produced", InferenceReview: "not_produced", RecommendationReview: "not_produced", InferenceCounts: map[string]int{}, RecommendationCounts: map[string]int{}, CriterionCounts: map[string]map[string]int{}}
	if out.Record.Recovery != nil && out.Record.Recovery.ModelOutputPresence == "unknown" {
		if review != nil {
			return score, ErrScoring
		}
		score.Status, score.InferenceReview, score.RecommendationReview = "unknown_pending_reconciliation", "unknown", "unknown"
		return score, nil
	}
	if out.Record.ModelLayer != "produced" || out.Record.ModelOutputAbsent {
		if review != nil {
			return score, ErrScoring
		}
		return score, nil
	}
	if review == nil {
		score.Status = "pending_independent_review"
		score.InferenceReview, score.RecommendationReview = "pending_independent_review", "pending_independent_review"
		return score, nil
	}
	if out.ModelOutput == nil || review.RunID != out.Record.RunID || review.ModelOutputRef != out.Record.ModelOutputRef || !publicRef.MatchString(review.Version) || !publicRef.MatchString(review.ReviewRef) || len(review.Recommendations) != len(out.ModelOutput.Recommendations) {
		return score, ErrScoring
	}
	score.Status, score.ReviewVersion, score.ReviewRef = "independently_reviewed", review.Version, review.ReviewRef
	score.InferenceReview, score.RecommendationReview = "independently_reviewed", "independently_reviewed"
	if !review.InferenceComplete {
		score.Status = "partially_reviewed"
		score.InferenceReview = "pending_independent_review"
	}
	if len(out.ModelOutput.Recommendations) == 0 {
		score.RecommendationReview = "not_produced"
	}
	assertions := map[string]bool{}
	for _, judgment := range review.Inferences {
		if !publicRef.MatchString(judgment.AssertionRef) || assertions[judgment.AssertionRef] || judgment.Kind != "candidate" && judgment.Kind != "assertion" && judgment.Kind != "kept_unconfirmed" || judgment.Verdict != "correct" && judgment.Verdict != "incorrect" && judgment.Verdict != "unknown" {
			return score, ErrScoring
		}
		assertions[judgment.AssertionRef] = true
		score.InferenceCounts[judgment.Kind+"_"+judgment.Verdict]++
	}
	if len(review.Inferences) == 0 {
		score.InferenceCounts["not_assessed"] = 1
	}
	indices := map[int]bool{}
	for _, judgment := range review.Recommendations {
		if judgment.Index < 0 || judgment.Index >= len(out.ModelOutput.Recommendations) || indices[judgment.Index] {
			return score, ErrScoring
		}
		indices[judgment.Index] = true
		values := map[string]string{"object_correct": judgment.ObjectCorrect, "evidence_supported": judgment.EvidenceSupported, "business_constraints": judgment.BusinessConstraints, "no_unauthorized_opening": judgment.NoUnauthorizedOpening}
		verdict := "satisfied"
		for key, value := range values {
			if !criterion(value) {
				return score, ErrScoring
			}
			if score.CriterionCounts[key] == nil {
				score.CriterionCounts[key] = map[string]int{}
			}
			score.CriterionCounts[key][value]++
			if value == "not_satisfied" {
				verdict = value
			} else if value == "unknown" && verdict == "satisfied" {
				verdict = "unknown"
			}
		}
		score.RecommendationCounts[verdict]++
	}
	return score, nil
}

// Summaries never discard failed/absent-output runs. Counts remain separate
// from scores, and a complete expected matrix is required to compute rates.
type RunCounts struct {
	Total                      int            `json:"total"`
	TerminalStates             map[string]int `json:"terminal_states"`
	OutputAbsent               int            `json:"output_absent"`
	ModelOutputAbsent          int            `json:"model_output_absent"`
	RuleOutputAbsent           int            `json:"rule_output_absent"`
	OutputPresenceUnknown      int            `json:"output_presence_unknown"`
	ModelOutputPresenceUnknown int            `json:"model_output_presence_unknown"`
	RuleOutputPresenceUnknown  int            `json:"rule_output_presence_unknown"`
	LogicalCountersUnknown     int            `json:"logical_counters_unknown"`
	ToolTraceUnknown           int            `json:"tool_trace_unknown"`
	StageTimingUnknown         int            `json:"stage_timing_unknown"`
	JournalInterruptedRuns     int            `json:"journal_interrupted_runs"`
	UsageUnknownRuns           int            `json:"usage_unknown_runs"`
	LedgerRecordedRuns         int            `json:"ledger_recorded_runs"`
	ModelHTTPAttempts          int64          `json:"model_http_attempts_known"`
	CloudHTTPAttempts          int64          `json:"cloud_http_attempts_known"`
	UnknownUsageAttempts       int64          `json:"unknown_usage_attempts"`
	KnownUsageResponses        int64          `json:"known_usage_responses"`
	CompletedRate              Ratio          `json:"completed_rate"`
	FailedRate                 Ratio          `json:"failed_rate"`
}
type Summary struct {
	ProtocolDigest   string            `json:"protocol_digest"`
	DatasetUse       string            `json:"dataset_use"`
	IndependentCases int               `json:"independent_cases"`
	Total            RunCounts         `json:"total"`
	ByArm            map[Arm]RunCounts `json:"by_arm"`
	RunIDs           []string          `json:"run_ids"`
	Qualification    string            `json:"qualification"`
}

func newCounts() RunCounts {
	return RunCounts{TerminalStates: map[string]int{"completed": 0, "partial": 0, "failed": 0, "cancelled": 0, "interrupted": 0}}
}
func addCount(c RunCounts, record RunRecord) RunCounts {
	c.Total++
	c.TerminalStates[record.Status]++
	if record.Recovery != nil {
		c.JournalInterruptedRuns++
		if record.Recovery.CountersStatus == "unknown" {
			c.LogicalCountersUnknown++
		}
		if record.Recovery.TraceStatus == "unknown" {
			c.ToolTraceUnknown++
		}
		if record.Recovery.TimingStatus == "unknown" {
			c.StageTimingUnknown++
		}
	}
	if record.Recovery != nil && record.Recovery.OutputPresence == "unknown" {
		c.OutputPresenceUnknown++
	} else if record.OutputAbsent {
		c.OutputAbsent++
	}
	if record.Recovery != nil && record.Recovery.ModelOutputPresence == "unknown" {
		c.ModelOutputPresenceUnknown++
	} else if record.ModelOutputAbsent {
		c.ModelOutputAbsent++
	}
	if record.Recovery != nil && record.Recovery.RuleOutputPresence == "unknown" {
		c.RuleOutputPresenceUnknown++
	} else if record.RuleAuditRef == "" {
		c.RuleOutputAbsent++
	}
	if record.Usage.Attempts == nil || record.Usage.Status != "ledger_recorded" {
		c.UsageUnknownRuns++
	} else {
		c.LedgerRecordedRuns++
		u := record.Usage.Attempts
		c.ModelHTTPAttempts += u.ModelAttempts
		c.CloudHTTPAttempts += u.CloudAttempts
		c.UnknownUsageAttempts += u.UnknownUsageAttempts
		c.KnownUsageResponses += u.KnownUsageResponses
	}
	c.CompletedRate, c.FailedRate = ratio(c.TerminalStates["completed"], c.Total), ratio(c.TerminalStates["failed"], c.Total)
	return c
}
func Aggregate(p Protocol, cases []FrozenCase, records []RunRecord) (Summary, error) {
	h, e := protocolDigest(p)
	if e != nil || len(cases) != len(p.Cases) {
		return Summary{}, ErrScoring
	}
	expected := 0
	byCase := map[string]FrozenCase{}
	for _, c := range cases {
		if _, duplicate := byCase[c.Definition.ID]; duplicate {
			return Summary{}, ErrScoring
		}
		byCase[c.Definition.ID] = c
		expected += 3 * c.Definition.Repetitions
	}
	if len(records) != expected {
		return Summary{}, ErrScoring
	}
	summary := Summary{ProtocolDigest: h, DatasetUse: p.DatasetUse, IndependentCases: len(cases), Total: newCounts(), ByArm: map[Arm]RunCounts{}, RunIDs: []string{}, Qualification: "All terminal states are retained. Shared rule metrics are scored once; model quality requires separate independent review. Missing usage stays unknown; attempt counts do not establish billing or Agent improvement."}
	for _, arm := range AllArms() {
		summary.ByArm[arm] = newCounts()
	}
	slots, ids, jobs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, record := range records {
		c, exists := byCase[record.CaseID]
		a := Assignment{RunID: record.RunID, CaseID: record.CaseID, Arm: record.Arm, Repetition: record.Repetition, JobID: record.JobID}
		key, _ := digest([]any{a.CaseID, a.Arm, a.Repetition})
		if !exists || !validAssignment(p, c, a) || !validEnd(record) || record.ProtocolVersion != p.Version || record.DatasetUse != p.DatasetUse || record.Split != c.Definition.Split || record.LabelVersionRef != c.Definition.LabelVersionRef || ids[a.RunID] || jobs[a.JobID] || slots[key] {
			return Summary{}, ErrScoring
		}
		conditions := record.Conditions
		if conditions.ProtocolDigest != h || conditions.InputDigest != c.InputDigest || conditions.CommonRuleDigest != c.CommonRuleDigest || !equalJSON(conditions.Workload, p.Workload) || conditions.ToolProjectionVersion != p.ToolProjectionVersion || conditions.CommonBriefVersion != p.CommonBriefVersion || len(conditions.ToolCatalogDigest) != 64 || len(conditions.FinalSchemaDigest) != 64 {
			return Summary{}, ErrScoring
		}
		if record.Arm == RulesOnly {
			if record.ModelCalls != 0 || record.ToolCalls != 0 || record.ModelLayer != "not_produced" || conditions.ModelConfigRef != "" || conditions.Sampling != nil || record.Usage.Attempts != nil && record.Usage.Attempts.ModelAttempts != 0 {
				return Summary{}, ErrScoring
			}
		} else {
			if conditions.ModelConfigRef != p.ModelConfigRef || conditions.Sampling == nil || *conditions.Sampling != p.Sampling {
				return Summary{}, ErrScoring
			}
			prompt := p.AgentPromptVersion
			if record.Arm == FixedModel {
				prompt = p.FixedPromptVersion
				if conditions.PipelineVersion != p.FixedPipeline.Version {
					return Summary{}, ErrScoring
				}
			}
			if conditions.PromptVersion != prompt {
				return Summary{}, ErrScoring
			}
		}
		ids[a.RunID], jobs[a.JobID], slots[key] = true, true, true
		summary.Total = addCount(summary.Total, record)
		summary.ByArm[record.Arm] = addCount(summary.ByArm[record.Arm], record)
		summary.RunIDs = append(summary.RunIDs, a.RunID)
	}
	// Equal catalog/schema digests are part of the comparison conditions.
	for _, record := range records {
		if record.Conditions.ToolCatalogDigest != records[0].Conditions.ToolCatalogDigest || record.Conditions.FinalSchemaDigest != records[0].Conditions.FinalSchemaDigest {
			return Summary{}, ErrScoring
		}
	}
	sort.Strings(summary.RunIDs)
	return summary, nil
}
