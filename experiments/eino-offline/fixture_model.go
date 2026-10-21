package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fixtureModel is a deterministic test driver, NOT an LLM. It contains scripted
// paths and deliberately injected errors; its choices cannot evidence autonomy.
type fixtureModel struct {
	state *runState
	tools []*schema.ToolInfo
}

var _ model.ToolCallingChatModel = (*fixtureModel)(nil)

func (m *fixtureModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &fixtureModel{state: m.state, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *fixtureModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("streaming is not implemented or validated in this experiment")
}

func (m *fixtureModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.state.opts.waitForCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s := m.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.report.ModelCalls++
	s.recordLocked("model_requested", "scripted_model", "")
	if s.report.ModelCalls > s.opts.MaxModelCalls {
		return nil, errors.New("model-call budget exceeded")
	}
	options := model.GetCommonOptions(&model.Options{Tools: m.tools}, opts...)
	toolNames := make(map[string]bool)
	for _, info := range options.Tools {
		toolNames[info.Name] = true
	}
	if len(options.Tools) != 2 || !toolNames["get_asset"] || !toolNames["get_business_intent"] {
		return nil, errors.New("expected exactly the two authorized tools")
	}
	if s.pendingID != "" {
		if len(input) == 0 {
			return nil, errors.New("missing tool result history")
		}
		last := input[len(input)-1]
		if last.Role != schema.Tool || last.ToolCallID != s.pendingID {
			return nil, errors.New("tool-call ID was not preserved in the result roundtrip")
		}
		var response lookupResult
		if err := strictJSON(last.Content, &response); err != nil {
			return nil, fmt.Errorf("invalid tool result: %w", err)
		}
		if s.evidence[response.EvidenceID] != s.pending {
			return nil, errors.New("tool result has incorrect evidence provenance")
		}
		s.report.ToolResultChecks++
		s.recordLocked("tool_result_received", s.pending, s.pendingID)
		s.pendingID, s.pending = "", ""
	}
	call := func(name, arguments string) (*schema.Message, error) {
		id := fmt.Sprintf("call-%d", s.report.ModelCalls)
		s.pendingID, s.pending = id, name
		s.recordLocked("tool_requested", name, id)
		return schema.AssistantMessage("", []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: arguments}}}), nil
	}
	args := `{"asset_id":"demo-web-001"}`
	switch s.opts.fault {
	case "unknown_tool":
		return call("delete_cloud_policy", args)
	case "bad_json":
		return call("get_asset", `{"asset_id":`)
	case "unknown_asset":
		return call("get_asset", `{"asset_id":"another-task-asset"}`)
	case "unknown_field":
		return call("get_asset", `{"asset_id":"demo-web-001","override_permissions":true}`)
	case "loop":
		return call("get_asset", args)
	}
	if s.opts.Scenario == "missing-whitelist" {
		if s.report.ModelCalls == 1 {
			return call("get_business_intent", args)
		}
	} else {
		if s.report.ModelCalls == 1 {
			return call("get_asset", args)
		}
		if s.report.ModelCalls == 2 {
			return call("get_business_intent", args)
		}
	}
	d := Decision{AssetID: assetID, EvidenceIDs: evidenceIDs(s)}
	if s.opts.Scenario == "missing-whitelist" {
		d.Status = "needs_confirmation"
		d.AdminSources = []string{}
		d.Conditions = []string{"样例没有提供管理来源，需要人工补充，不得编造白名单。"}
	} else {
		d.Status = "conditional_recommendation"
		d.AdminSources = []string{"198.51.100.10/32"}
		d.Conditions = []string{"仅建议收敛到给定来源；仍需核对规则顺序与默认行为，不代表其他来源已拒绝或服务实际可达。"}
	}
	switch s.opts.fault {
	case "invented_evidence":
		d.EvidenceIDs = append(d.EvidenceIDs, "ev-not-returned")
	case "invented_sources":
		d.AdminSources = []string{"0.0.0.0/0"}
	case "missing_conditions":
		d.Conditions = nil
	case "bad_output":
		return schema.AssistantMessage("not JSON", nil), nil
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	s.recordLocked("model_completed", "scripted_model", "")
	return schema.AssistantMessage(string(encoded), nil), nil
}
