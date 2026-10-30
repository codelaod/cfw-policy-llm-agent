package evaluation

import (
	"context"
	"fmt"
	"testing"
)

func TestIndependentFormalScorerRequiresLabelsForEveryFrozenCase(t *testing.T) {
	p, c, labels := scoringFixture(t)
	p.DatasetUse = "formal"
	p.Cases = nil
	cases := []FrozenCase{}
	for i := 0; i < 20; i++ {
		d := c.Definition
		d.ID = fmt.Sprintf("formal-%02d", i)
		d.ScenarioRef = fmt.Sprintf("scenario-%02d", i)
		d.Split = "formal"
		d.LabelReviewState = "human_reviewed"
		d.LabelReviewRef = "artificial-test-declaration-only"
		p.Cases = append(p.Cases, d)
		in, err := c.Input()
		if err != nil {
			t.Fatal(err)
		}
		frozen, err := FreezeCase(context.Background(), d, in, p.Workload)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, frozen)
	}
	labels.ReviewState = "human_reviewed"
	labels.ReviewRef = "artificial-test-declaration-only"
	for i := range labels.Facts {
		labels.Facts[i].Key.CaseID = cases[0].Definition.ID
	}
	h, err := protocolDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	labels.ProtocolDigest = h
	scorer, err := NewScorer(p, labels)
	if err != nil {
		return
	}
	score, err := scorer.ScoreRules(cases)
	if err == nil {
		t.Fatalf("formal reviewed scorer accepted truth for only 1 of 20 cases: raw=%d unlabelled=%d review=%s precision=%+v recall=%+v", score.RawFindings, score.UnlabelledOutputs, score.ReviewState, score.Precision, score.Recall)
	}
}
