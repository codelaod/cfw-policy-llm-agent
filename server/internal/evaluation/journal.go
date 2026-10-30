package evaluation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"cfw-policy-llm-agent/server/internal/sensitive"
)

const maxJournalLine = 2 << 20

type journalEntry struct {
	Type  string     `json:"type"`
	Start *RunStart  `json:"start,omitempty"`
	End   *RunRecord `json:"end,omitempty"`
}
type journalState struct {
	start RunStart
	end   *RunRecord
}

// FileJournal is an exclusively locked, append-only private JSONL journal.
// OpenFileJournal never resumes work. An unfinished start is durably closed as
// interrupted before the journal can admit a new run.
type FileJournal struct {
	mu        sync.Mutex
	file      *os.File
	states    map[string]journalState
	sensitive []string
	broken    bool
}

func OpenFileJournal(path string, sensitiveValues ...string) (*FileJournal, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if e != nil {
		return nil, ErrJournal
	}
	fail := func() (*FileJournal, error) { _ = f.Close(); return nil, ErrJournal }
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fail()
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return fail()
	}
	j := &FileJournal{file: f, states: map[string]journalState{}, sensitive: append([]string(nil), sensitiveValues...)}
	if info.Size() > 0 {
		last := make([]byte, 1)
		if _, e = f.ReadAt(last, info.Size()-1); e != nil || last[0] != '\n' {
			return fail()
		}
	}
	r := bufio.NewScanner(f)
	r.Buffer(make([]byte, 64<<10), maxJournalLine)
	for r.Scan() {
		line := r.Bytes()
		if len(bytes.TrimSpace(line)) == 0 || !j.safe(line) {
			return fail()
		}
		var entry journalEntry
		d := json.NewDecoder(bytes.NewReader(line))
		d.DisallowUnknownFields()
		if d.Decode(&entry) != nil || d.Decode(new(any)) != io.EOF {
			return fail()
		}
		canonical, e := json.Marshal(entry)
		if e != nil || !bytes.Equal(line, canonical) || j.load(entry) != nil {
			return fail()
		}
	}
	if r.Err() != nil {
		return fail()
	}
	ids := []string{}
	for id, state := range j.states {
		if state.end == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		state := j.states[id]
		record := state.start.Record
		record.Status, record.ErrorCode, record.OutputAbsent = "interrupted", "EVALUATION_RESTART_INTERRUPTED", false
		record.ModelLayer, record.ModelOutputAbsent = "unknown", false
		record.InferenceReview, record.RecommendationReview = "unknown", "unknown"
		record.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		record.Recovery = &Recovery{State: "pending_store_reconciliation", JournalStatus: record.Status, JournalErrorCode: record.ErrorCode, DetectedAt: record.FinishedAt, OutputPresence: "unknown", RuleOutputPresence: "unknown", ModelOutputPresence: "unknown", CountersStatus: "unknown", TraceStatus: "unknown", TimingStatus: "unknown"}
		// A process interruption leaves physical request delivery and usage
		// unknown. Recovery must not manufacture a zero-attempt outcome.
		record.Usage = Usage{Status: "unknown"}
		if j.append(journalEntry{Type: "end", End: &record}) != nil {
			return fail()
		}
		state.end = &record
		j.states[id] = state
	}
	return j, nil
}

func (j *FileJournal) safe(raw []byte) bool {
	_, changed := sensitive.Redact(string(raw), j.sensitive)
	return !changed
}
func (j *FileJournal) load(e journalEntry) error {
	switch e.Type {
	case "start":
		if e.Start == nil || e.End != nil || !validStart(*e.Start) {
			return ErrJournal
		}
		id := e.Start.Record.RunID
		if _, exists := j.states[id]; exists {
			return ErrJournal
		}
		j.states[id] = journalState{start: *e.Start}
	case "end":
		if e.End == nil || e.Start != nil || !validEnd(*e.End) {
			return ErrJournal
		}
		state, exists := j.states[e.End.RunID]
		if !exists || state.end != nil || !sameRun(state.start.Record, *e.End) {
			return ErrJournal
		}
		state.end = e.End
		j.states[e.End.RunID] = state
	case "reconcile":
		if e.End == nil || e.Start != nil || !validEnd(*e.End) {
			return ErrJournal
		}
		state, exists := j.states[e.End.RunID]
		if !exists || state.end == nil || !validReconciliation(*state.end, *e.End) || !sameRun(state.start.Record, *e.End) {
			return ErrJournal
		}
		state.end = e.End
		j.states[e.End.RunID] = state
	default:
		return ErrJournal
	}
	return nil
}
func validStart(s RunStart) bool {
	return validRunIdentity(s.Record) && s.Record.Status == "running" && s.Record.OutputAbsent && s.Record.FinishedAt == "" && shaDigest.MatchString(s.RequestDigest)
}
func validEnd(r RunRecord) bool {
	_, e := time.Parse(time.RFC3339Nano, r.FinishedAt)
	return validRunIdentity(r) && terminal(r.Status) && e == nil && r.ExecutionMillis >= 0 && r.ModelStageMillis >= 0 && r.FinalizeMillis >= 0 && r.ModelCalls >= 0 && r.ToolCalls >= 0
}
func validRunIdentity(r RunRecord) bool {
	_, e := time.Parse(time.RFC3339Nano, r.StartedAt)
	if r.SchemaVersion != 1 || !publicRef.MatchString(r.RunID) || !publicRef.MatchString(r.CaseID) || !publicRef.MatchString(r.ScenarioRef) || !publicRef.MatchString(r.JobID) || !publicRef.MatchString(r.ProtocolVersion) || !publicRef.MatchString(r.LabelVersionRef) || r.Repetition < 1 || !validArm(r.Arm) || e != nil || r.DatasetUse != "development" && r.DatasetUse != "formal" || r.Split != "development" && r.Split != "formal" || r.ErrorCode != "" && !controlledError.MatchString(r.ErrorCode) || r.ModelLayer != "produced" && r.ModelLayer != "not_produced" && r.ModelLayer != "unknown" || r.ModelOutputAbsent != (r.ModelLayer == "not_produced") || r.QueueMillis != nil && *r.QueueMillis < 0 {
		return false
	}
	if r.Recovery != nil {
		if !validRecovery(r) {
			return false
		}
	} else if r.ModelLayer == "unknown" {
		return false
	}
	for _, ref := range []string{r.RuleAuditRef, r.ModelOutputRef, r.ReportRef} {
		if ref != "" && !publicRef.MatchString(ref) {
			return false
		}
	}
	if r.Usage.Status == "unknown" {
		if r.Usage.Attempts != nil {
			return false
		}
	} else if r.Usage.Status == "ledger_recorded" {
		u := r.Usage.Attempts
		if u == nil || u.CloudAttempts < 0 || u.ModelAttempts < 0 || u.UnknownDelivery < 0 || u.KnownUsageResponses < 0 || u.UnknownUsageAttempts < 0 || u.ReportedTokens != nil && !u.ReportedTokens.Valid() {
			return false
		}
	} else {
		return false
	}
	calls := map[string]bool{}
	for i, trace := range r.Traces {
		if trace.Sequence != i+1 || !publicRef.MatchString(trace.CallID) || !publicRef.MatchString(trace.Tool) || calls[trace.CallID] || trace.ElapsedMS < 0 || trace.SelectionOrigin != "model" && trace.SelectionOrigin != "predefined_pipeline" {
			return false
		}
		switch trace.Status {
		case "started", "succeeded", "failed", "cancelled", "truncated", "rejected":
		default:
			return false
		}
		for _, ref := range append(append([]string{}, trace.InputRefs...), trace.EvidenceRefs...) {
			if !publicRef.MatchString(ref) {
				return false
			}
		}
		for _, call := range trace.DependsOn {
			if !calls[call] {
				return false
			}
		}
		calls[trace.CallID] = true
	}
	return len(r.Traces) <= r.ToolCalls
}

func validRecovery(r RunRecord) bool {
	v := r.Recovery
	if v == nil || v.JournalStatus != "interrupted" || v.JournalErrorCode != "EVALUATION_RESTART_INTERRUPTED" || v.State != "pending_store_reconciliation" && v.State != "store_partial" && v.State != "store_reconciled" {
		return false
	}
	if _, e := time.Parse(time.RFC3339Nano, v.DetectedAt); e != nil {
		return false
	}
	if v.CheckedAt != "" {
		if _, e := time.Parse(time.RFC3339Nano, v.CheckedAt); e != nil {
			return false
		}
	}
	for _, presence := range []string{v.OutputPresence, v.RuleOutputPresence, v.ModelOutputPresence} {
		if presence != "present" && presence != "absent" && presence != "unknown" {
			return false
		}
	}
	if r.OutputAbsent != (v.OutputPresence == "absent") || r.ModelOutputAbsent != (v.ModelOutputPresence == "absent") || (r.ModelLayer == "unknown") != (v.ModelOutputPresence == "unknown") {
		return false
	}
	if v.CountersStatus != "unknown" && v.CountersStatus != "agent_recorded" && v.CountersStatus != "rules_only_zero" {
		return false
	}
	if v.TraceStatus != "unknown" || v.TimingStatus != "unknown" {
		return false
	}
	return true
}
func needsReconciliation(r RunRecord) bool {
	return r.ErrorCode == "EVALUATION_RESTART_INTERRUPTED" && r.Recovery == nil || r.Recovery != nil && r.Recovery.State != "store_reconciled"
}
func validReconciliation(original, updated RunRecord) bool {
	if !needsReconciliation(original) || updated.Recovery == nil || updated.Recovery.State == "pending_store_reconciliation" || !sameRun(original, updated) {
		return false
	}
	detected := original.FinishedAt
	if original.Recovery != nil {
		detected = original.Recovery.DetectedAt
	}
	return updated.Recovery.DetectedAt == detected && updated.Recovery.CheckedAt != ""
}
func sameRun(a, b RunRecord) bool {
	return a.SchemaVersion == b.SchemaVersion && a.RunID == b.RunID && a.CaseID == b.CaseID && a.ScenarioRef == b.ScenarioRef && a.JobID == b.JobID && a.Arm == b.Arm && a.Repetition == b.Repetition && a.ProtocolVersion == b.ProtocolVersion && a.LabelVersionRef == b.LabelVersionRef && a.DatasetUse == b.DatasetUse && a.Split == b.Split && a.StartedAt == b.StartedAt && equalJSON(a.Conditions, b.Conditions)
}
func equalJSON(a, b any) bool {
	x, e := digest(a)
	y, f := digest(b)
	return e == nil && f == nil && x == y
}
func (j *FileJournal) append(entry journalEntry) error {
	if j.file == nil || j.broken {
		return ErrJournal
	}
	raw, e := json.Marshal(entry)
	if e != nil || len(raw)+1 > maxJournalLine || !j.safe(raw) {
		return ErrJournal
	}
	raw = append(raw, '\n')
	n, e := j.file.Write(raw)
	if e != nil || n != len(raw) || j.file.Sync() != nil {
		j.broken = true
		return ErrJournal
	}
	return nil
}
func (j *FileJournal) Begin(ctx context.Context, start RunStart) (*RunRecord, bool, error) {
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !validStart(start) || j.broken || j.file == nil {
		return nil, false, ErrJournal
	}
	if state, exists := j.states[start.Record.RunID]; exists {
		if state.start.RequestDigest != start.RequestDigest {
			return nil, false, ErrReplay
		}
		if state.end == nil {
			return nil, false, ErrReplay
		}
		copy, e := copyJSON(*state.end)
		if e != nil {
			return nil, false, ErrJournal
		}
		return &copy, false, nil
	}
	detached, e := copyJSON(start)
	if e != nil || j.append(journalEntry{Type: "start", Start: &detached}) != nil {
		return nil, false, ErrJournal
	}
	j.states[start.Record.RunID] = journalState{start: detached}
	return nil, true, nil
}
func (j *FileJournal) End(ctx context.Context, record RunRecord) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	state, exists := j.states[record.RunID]
	if !exists || !validEnd(record) || !sameRun(state.start.Record, record) {
		return ErrJournal
	}
	if state.end != nil {
		if equalJSON(*state.end, record) {
			return nil
		}
		return ErrReplay
	}
	detached, e := copyJSON(record)
	if e != nil || j.append(journalEntry{Type: "end", End: &detached}) != nil {
		return ErrJournal
	}
	state.end = &detached
	j.states[record.RunID] = state
	return nil
}

// Reconcile appends locally verified products after an interrupted journal end;
// it never overwrites that interruption or creates a fresh execution start.
func (j *FileJournal) Reconcile(ctx context.Context, original, record RunRecord) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	state, exists := j.states[record.RunID]
	if !exists || state.end == nil || !equalJSON(*state.end, original) || !validEnd(record) || !validReconciliation(original, record) {
		return ErrReplay
	}
	detached, e := copyJSON(record)
	if e != nil || j.append(journalEntry{Type: "reconcile", End: &detached}) != nil {
		return ErrJournal
	}
	state.end = &detached
	j.states[record.RunID] = state
	return nil
}
func (j *FileJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	if err != nil {
		return ErrJournal
	}
	return nil
}
