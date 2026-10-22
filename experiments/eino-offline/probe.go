// This experiment exercises Eino with synthetic fixtures in scripted or explicit live mode.
// It is not a full protocol, production-audit or firewall-correctness evaluation.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const assetID = "demo-web-001"

// Options contains execution bounds, not model sampling or provider settings.
type Options struct {
	Scenario       string
	MaxIterations  int
	MaxModelCalls  int
	MaxToolCalls   int
	MaxOutputBytes int
	MaxToolBytes   int
	fault          string // Only tests inject failures.
	waitForCancel  bool
	failTool       string
}

func defaults() Options {
	return Options{Scenario: "whitelist", MaxIterations: 4, MaxModelCalls: 4,
		MaxToolCalls: 3, MaxOutputBytes: 16384, MaxToolBytes: 4096}
}

type Decision struct {
	Status       string   `json:"status"`
	AssetID      string   `json:"asset_id"`
	EvidenceIDs  []string `json:"evidence_ids"`
	AdminSources []string `json:"admin_sources"`
	Conditions   []string `json:"conditions"`
}

type Event struct {
	Sequence int    `json:"sequence"`
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	CallID   string `json:"call_id,omitempty"`
}

type ReportedTokenUsage struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	TotalTokens        int `json:"total_tokens"`
	ResponsesWithUsage int `json:"responses_with_usage"`
}

type Report struct {
	Mode               string              `json:"mode"`
	NetworkRequests    int                 `json:"network_request_attempts,omitempty"`
	HTTPStatus         int                 `json:"last_http_status,omitempty"`
	ErrorCode          string              `json:"error_code,omitempty"`
	FinishReasons      []string            `json:"finish_reasons,omitempty"`
	TokenUsage         *ReportedTokenUsage `json:"reported_token_usage,omitempty"`
	Scenario           string              `json:"scenario"`
	Status             string              `json:"status"`
	ModelCalls         int                 `json:"model_calls"`
	ToolCalls          int                 `json:"tool_calls"`
	ToolResultChecks   int                 `json:"tool_result_roundtrips"`
	Decision           *Decision           `json:"decision,omitempty"`
	Events             []Event             `json:"events"`
	Error              string              `json:"error,omitempty"`
	ValidationBoundary string              `json:"validation_boundary"`
}

type lookupRequest struct {
	AssetID string `json:"asset_id" jsonschema:"description=Authorized synthetic asset identifier"`
}

type lookupResult struct {
	AssetID      string   `json:"asset_id"`
	Purpose      string   `json:"purpose,omitempty"`
	AdminSources []string `json:"admin_sources,omitempty"`
	Missing      bool     `json:"missing"`
	EvidenceID   string   `json:"evidence_id"`
	Source       string   `json:"source"`
}

type runState struct {
	mu          sync.Mutex
	opts        Options
	report      Report
	evidence    map[string]string // Only successfully returned tool evidence.
	pendingID   string
	pending     string
	livePending map[string]string
	liveCallIDs map[string]bool
}

func (s *runState) recordLocked(kind, name, callID string) {
	s.report.Events = append(s.report.Events, Event{Sequence: len(s.report.Events) + 1, Kind: kind, Name: name, CallID: callID})
}

// domainTool keeps permission, argument and output bounds outside the framework.
type domainTool struct {
	name  string
	info  *schema.ToolInfo
	state *runState
}

func (t *domainTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }

func (t *domainTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (result string, err error) {
	s := t.state
	s.mu.Lock()
	defer s.mu.Unlock()
	s.report.ToolCalls++
	callID := compose.GetToolCallID(ctx)
	if callID == "" {
		callID = s.pendingID
	}
	s.recordLocked("tool_started", t.name, callID)
	defer func() {
		kind := "tool_succeeded"
		if err != nil {
			kind = "tool_failed"
		}
		s.recordLocked(kind, t.name, callID)
	}()
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if s.report.ToolCalls > s.opts.MaxToolCalls {
		return "", errors.New("tool-call budget exceeded")
	}
	if len(arguments) > 4096 {
		return "", errors.New("tool arguments exceed byte limit")
	}
	var request lookupRequest
	if err = strictJSON(arguments, &request); err != nil {
		return "", fmt.Errorf("invalid tool arguments: %w", err)
	}
	if request.AssetID != assetID {
		return "", errors.New("asset outside current task scope")
	}
	if s.opts.failTool == t.name {
		return "", errors.New("synthetic tool failure")
	}
	response := lookupResult{AssetID: assetID}
	switch t.name {
	case "get_asset":
		response.Purpose = "synthetic public Web service"
		response.EvidenceID = "ev-asset"
		response.Source = "synthetic://fixtures/assets/" + assetID
	case "get_business_intent":
		response.Missing = s.opts.Scenario == "missing-whitelist"
		response.EvidenceID = "ev-intent"
		response.Source = "synthetic://fixtures/intents/" + assetID
		if !response.Missing {
			response.AdminSources = []string{"198.51.100.10/32"}
		}
	default:
		return "", errors.New("tool not in allowlist")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	if len(encoded) > s.opts.MaxToolBytes {
		return "", errors.New("tool result exceeds byte limit")
	}
	s.evidence[response.EvidenceID] = t.name
	return string(encoded), nil
}

func strictJSON(text string, value any) error {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func validateDecision(text string, s *runState) (*Decision, error) {
	if len(text) > s.opts.MaxOutputBytes {
		return nil, errors.New("final output exceeds byte limit")
	}
	var d Decision
	if err := strictJSON(text, &d); err != nil {
		return nil, fmt.Errorf("invalid final JSON: %w", err)
	}
	if d.AssetID != assetID {
		return nil, errors.New("final asset outside task scope")
	}
	if len(d.Conditions) == 0 || len(d.EvidenceIDs) == 0 {
		return nil, errors.New("conditions and evidence are required")
	}
	seen := make(map[string]bool)
	for _, id := range d.EvidenceIDs {
		if seen[id] {
			return nil, errors.New("duplicate evidence reference")
		}
		seen[id] = true
		if _, ok := s.evidence[id]; !ok {
			return nil, errors.New("evidence was not returned by a successful tool")
		}
	}
	if !seen["ev-intent"] {
		return nil, errors.New("business-intent evidence is required")
	}
	switch s.opts.Scenario {
	case "missing-whitelist":
		if d.Status != "needs_confirmation" || len(d.AdminSources) != 0 {
			return nil, errors.New("missing whitelist must remain unconfirmed without invented sources")
		}
	case "whitelist":
		if d.Status != "conditional_recommendation" || !seen["ev-asset"] || len(d.AdminSources) != 1 {
			return nil, errors.New("recommendation requires asset evidence and the declared source")
		}
		p, err := netip.ParsePrefix(d.AdminSources[0])
		if err != nil || p.String() != "198.51.100.10/32" {
			return nil, errors.New("recommended source differs from synthetic business intent")
		}
	}
	return &d, nil
}

func runProbe(ctx context.Context, opts Options) (Report, error) {
	return runProbeWithFactory(ctx, opts, "scripted_model", func(s *runState) model.BaseChatModel { return &fixtureModel{state: s} })
}

func runProbeWithFactory(ctx context.Context, opts Options, mode string, factory func(*runState) model.BaseChatModel) (Report, error) {
	if opts.Scenario != "whitelist" && opts.Scenario != "missing-whitelist" {
		return Report{}, errors.New("unknown scenario")
	}
	if opts.MaxIterations < 1 || opts.MaxModelCalls < 1 || opts.MaxToolCalls < 1 || opts.MaxOutputBytes < 1 || opts.MaxToolBytes < 1 {
		return Report{}, errors.New("all execution limits must be positive")
	}
	s := &runState{opts: opts, evidence: make(map[string]string), report: Report{
		Mode: mode, Scenario: opts.Scenario, Status: "running",
		ValidationBoundary: "synthetic Eino integration only; not real-model compatibility, autonomous decision quality, cloud API or firewall correctness validation",
	}}
	if mode == "live_model" {
		s.report.ValidationBoundary = "live model with synthetic fixtures only; non-streaming; prompt-constrained JSON with local validation; no cloud, production audit or reachability verification"
	}
	fail := func(err error) (Report, error) {
		s.report.Status = "failed"
		if mode == "live_model" {
			safe := sanitizedLiveError(err)
			s.report.ErrorCode = safe.code
			s.report.Error = safe.Error()
			err = safe
		} else {
			s.report.Error = err.Error()
		}
		return s.report, err
	}
	var tools []tool.BaseTool
	for _, name := range []string{"get_asset", "get_business_intent"} {
		info, err := utils.GoStruct2ToolInfo[lookupRequest](name, "Read the current task's synthetic fixture only")
		if err != nil {
			return fail(err)
		}
		tools = append(tools, &domainTool{name: name, info: info, state: s})
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "offline_audit_probe", Description: "scripted-model framework integration probe",
		Instruction: probeInstruction(mode),
		Model:       factory(s), MaxIterations: opts.MaxIterations,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools}},
	})
	if err != nil {
		return fail(err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: false})
	iterator := runner.Run(ctx, []*schema.Message{schema.UserMessage("Review synthetic asset " + assetID)})
	var final string
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			err = event.Err
			continue
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message := event.Output.MessageOutput.Message
		if message != nil && message.Role == schema.Assistant && len(message.ToolCalls) == 0 {
			final = message.Content
		}
	}
	if err != nil {
		return fail(err)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	if mode == "live_model" {
		final = finalContentWithoutThinking(final)
	}
	decision, err := validateDecision(final, s)
	if err != nil {
		return fail(err)
	}
	s.report.Decision = decision
	s.report.Status = "completed"
	return s.report, nil
}

func evidenceIDs(s *runState) []string {
	ids := make([]string, 0, len(s.evidence))
	for id := range s.evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func probeInstruction(mode string) string {
	if mode != "live_model" {
		return "Read authorized fixtures only. Cite evidence. Missing intent stays unconfirmed. Configuration is not proof of reachability."
	}
	return `You are testing a read-only audit tool protocol using synthetic data, not production assets.
The only authorized asset is demo-web-001. Obtain business intent through tools; never invent sources or evidence.
Use at most one tool per assistant turn; only query information necessary for the task. Do not emit narration or thinking in the final answer.
Final answer must be one bare JSON object with exactly these fields:
{"status":"conditional_recommendation or needs_confirmation","asset_id":"demo-web-001","evidence_ids":["IDs actually returned by successful tools"],"admin_sources":["exact declared CIDRs, or empty if missing"],"conditions":["short prerequisite/impact statement"]}
If a management whitelist is present, verify the asset's purpose too, cite both asset and intent evidence, and recommend only the exact declared sources.
If no whitelist is provided, stop with needs_confirmation and empty admin_sources; cite intent evidence and ask for the missing business requirement.
Do not claim that shrinking an allow rule blocks other sources or proves reachability; rule order, remaining paths and default behavior still need checking.
No other tools, external data, cloud writes or arbitrary network actions are available.`
}
