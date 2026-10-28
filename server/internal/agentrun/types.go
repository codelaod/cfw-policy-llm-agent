// Package agentrun executes one bounded, non-streaming Eino agent per task.
// Business persistence, outbound admission and domain validation stay outside it.
package agentrun

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Query interface {
	InitialContext() string
	BeginTurn()
	Tools() []*schema.ToolInfo
	Invoke(ctx context.Context, name, args string) (string, error)
	ValidateFinal(text string) (json.RawMessage, error)
}

type HistoryMessage struct {
	Role string
	Text string
}
type ModelFactory func(context.Context) (model.BaseChatModel, error)
type Task struct {
	Kind     string
	History  []HistoryMessage
	Queries  Query
	NewModel ModelFactory
}

type Config struct {
	SensitiveValues []string
	MaxModelCalls   int
	MaxToolCalls    int
	ModelTimeout    time.Duration
	FinalSchema     json.RawMessage
}

type Event struct {
	Type         string   `json:"type"`
	ToolName     string   `json:"tool_name"`
	CallID       string   `json:"call_id"`
	Status       string   `json:"status"`
	ElapsedMS    int64    `json:"elapsed_ms"`
	EvidenceRefs []string `json:"evidence_refs"`
}
type Sink interface {
	Record(context.Context, Event) error
}
type SinkFunc func(context.Context, Event) error

func (f SinkFunc) Record(ctx context.Context, event Event) error { return f(ctx, event) }

type Outcome struct {
	Output     json.RawMessage `json:"output,omitempty"`
	ModelCalls int             `json:"model_calls"`
	ToolCalls  int             `json:"tool_calls"`
	UsageKnown bool            `json:"usage_known"`
	ErrorCode  string          `json:"error_code,omitempty"`
}

// Error carries a controlled code only. It deliberately does not wrap raw
// model, query or persistence errors, which may contain private request values.
type Error struct{ Code string }

func (e *Error) Error() string     { return e.Code }
func (e *Error) ErrorCode() string { return e.Code }

var safeCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Controlled allows adapters to expose a deliberately selected safe code.
func Controlled(code string) error {
	if !safeCode.MatchString(code) {
		code = "INTERNAL_ERROR"
	}
	return &Error{Code: code}
}
