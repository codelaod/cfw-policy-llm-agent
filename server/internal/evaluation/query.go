package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/agentrun"
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// queryAdapter preserves the production dependency boundary: only the runner
// calls BeginTurn. Trace bookkeeping never promotes new tool references.
type queryAdapter struct {
	service *agentquery.Service
	tools   []*schema.ToolInfo
	trace   *traceSink
}

var projectedRef = regexp.MustCompile(`^[a-z][a-z_]*_[A-Za-z0-9]{16}_[0-9]+$`)

func newQueryAdapter(service *agentquery.Service, sink *traceSink) (*queryAdapter, error) {
	a := &queryAdapter{service: service, trace: sink}
	for _, definition := range service.Tools() {
		var params jsonschema.Schema
		if json.Unmarshal(definition.Parameters, &params) != nil {
			return nil, ErrProtocol
		}
		a.tools = append(a.tools, &schema.ToolInfo{Name: definition.Name, Desc: definition.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&params)})
	}
	return a, nil
}
func (a *queryAdapter) InitialContext() string    { return a.service.InitialContext() }
func (a *queryAdapter) BeginTurn()                { a.service.BeginTurn() }
func (a *queryAdapter) Tools() []*schema.ToolInfo { return a.tools }
func (a *queryAdapter) Invoke(ctx context.Context, name, args string) (string, error) {
	result, e := a.service.Invoke(ctx, name, args)
	if e == nil {
		// Only arguments accepted by the actual service are eligible for the
		// trace. Rejected model arguments may contain secrets or arbitrary URLs.
		a.trace.accepted(name, args, result, a.service.LocalBindings())
		return result, nil
	}
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return "", e
	}
	switch {
	case errors.Is(e, agentquery.ErrDenied):
		return "", agentrun.Controlled("QUERY_REFERENCE_DENIED")
	case errors.Is(e, agentquery.ErrTooLarge):
		return "", agentrun.Controlled("QUERY_OUTPUT_TOO_LARGE")
	case errors.Is(e, agentquery.ErrUnknownTool):
		return "", agentrun.Controlled("QUERY_TOOL_UNKNOWN")
	default:
		return "", agentrun.Controlled("QUERY_INPUT_INVALID")
	}
}
func (a *queryAdapter) ValidateFinal(text string) (json.RawMessage, error) {
	output, e := a.service.ValidateFinal(text)
	if e != nil {
		return nil, agentrun.Controlled("MODEL_OUTPUT_INVALID")
	}
	return json.Marshal(output)
}

type traceSink struct {
	mu      sync.Mutex
	store   *storage.Store
	jobID   string
	origin  string
	traces  []ToolTrace
	active  int
	origins map[string]string
	initial map[string]bool
}

func (s *traceSink) Record(ctx context.Context, event agentrun.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload := map[string]any{"phase": "L3", "tool": event.ToolName, "call_id": event.CallID, "status": event.Status, "elapsed_ms": event.ElapsedMS, "evidence_refs": event.EvidenceRefs, "selection_origin": s.origin}
	if e := s.store.AppendJobEvent(ctx, s.jobID, event.Type, payload); e != nil {
		return agentrun.Controlled("EVENT_SAVE_FAILED")
	}
	if event.Type == "tool_started" {
		s.traces = append(s.traces, ToolTrace{Sequence: len(s.traces) + 1, CallID: event.CallID, Tool: event.ToolName, SelectionOrigin: s.origin, Status: "started", InputRefs: []string{}, DependsOn: []string{}, EvidenceRefs: []string{}})
		s.active = len(s.traces) - 1
	} else if event.Type == "tool_finished" {
		for i := len(s.traces) - 1; i >= 0; i-- {
			if s.traces[i].CallID == event.CallID {
				s.traces[i].Status, s.traces[i].ElapsedMS = event.Status, event.ElapsedMS
				s.traces[i].EvidenceRefs = append([]string{}, event.EvidenceRefs...)
				break
			}
		}
	}
	return nil
}
func (s *traceSink) accepted(name, args, result string, bindings map[string]agentquery.LocalBinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active < 0 || s.active >= len(s.traces) || s.traces[s.active].Tool != name {
		return
	}
	trace := &s.traces[s.active]
	var values map[string]string
	if json.Unmarshal([]byte(args), &values) == nil {
		for key, ref := range values {
			_, bound := bindings[ref]
			if publicRef.MatchString(ref) && (bound || key == "rule_id") {
				trace.InputRefs = append(trace.InputRefs, ref)
				if id := s.origins[ref]; id != "" && id != trace.CallID {
					trace.DependsOn = append(trace.DependsOn, id)
				}
			}
		}
	}
	trace.InputRefs = sortedUnique(trace.InputRefs)
	trace.DependsOn = sortedUnique(trace.DependsOn)
	var tree any
	if json.Unmarshal([]byte(result), &tree) != nil {
		return
	}
	var visit func(any)
	visit = func(v any) {
		switch item := v.(type) {
		case string:
			if projectedRef.MatchString(item) && !s.initial[item] && s.origins[item] == "" {
				s.origins[item] = trace.CallID
			}
		case []any:
			for _, child := range item {
				visit(child)
			}
		case map[string]any:
			for key, child := range item {
				if strings.Contains(key, "truncated") && child == true {
					trace.Truncated = true
				}
				visit(child)
			}
		}
	}
	visit(tree)
}
func (s *traceSink) snapshot() []ToolTrace {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, _ := copyJSON(s.traces)
	if out == nil {
		out = []ToolTrace{}
	}
	return out
}
func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
