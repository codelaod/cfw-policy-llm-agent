package agentrun

import (
	"context"
	"encoding/json"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

func TestServerOutputContractIsIncludedInSystemInstruction(t *testing.T) {
	q := newQuery("contract")
	seen := false
	input := task(q, func(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		for _, message := range messages {
			if message.Role == schema.System && strings.Contains(message.Content, "server_output_contract_marker") {
				seen = true
			}
		}
		return answer(final("contract")), nil
	})
	_, err := Run(context.Background(), input, Config{FinalSchema: json.RawMessage(`{"type":"object","description":"server_output_contract_marker"}`)}, SinkFunc(func(context.Context, Event) error { return nil }))
	if err != nil || !seen {
		t.Fatal("model never received the server-owned final JSON contract", err)
	}
}
