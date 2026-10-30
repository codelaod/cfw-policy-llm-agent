package app

import (
	"context"
	"errors"
	"time"

	"cfw-policy-llm-agent/server/internal/storage"
)

// runBatch owns one worker and one accepted root. Internal segments are invoked
// synchronously; each keeps its actual immutable input, result and fixed report.
func (w *Workflow) runBatch(ctx context.Context, parent storage.Job, p storage.ExecutionPlan) error {
	closeFailure := func(err error) error {
		status, code := "failed", "BATCH_EXECUTION_FAILED"
		if errors.Is(err, context.Canceled) {
			status, code = "cancelled", "CANCELLED"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			status, code = "failed", "JOB_DEADLINE_EXCEEDED"
		}
		if errors.Is(context.Cause(ctx), ErrStopping) {
			status, code = "interrupted", "JOB_INTERRUPTED"
		}
		var failure JobFailure
		if errors.As(err, &failure) {
			code = failure.Code
			if failure.Partial {
				status = "partial"
			}
		}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Stop is metadata only and cannot start a segment or provider attempt.
		_ = w.Store.StopBatch(saveCtx, parent.ID, status, code)
		return err
	}
	for {
		if e := ctx.Err(); e != nil {
			return closeFailure(e)
		}
		child, found, e := w.Store.StartNextBatchSegment(ctx, parent.ID)
		if e != nil {
			return closeFailure(e)
		}
		if !found {
			break
		}
		deadline, e := time.Parse(time.RFC3339Nano, child.ExecutionDeadline)
		if e != nil {
			return closeFailure(JobFailure{Code: "BATCH_PLAN_INVALID"})
		}
		childCtx, cancel := context.WithDeadline(ctx, deadline)
		runErr := w.Run(childCtx, child)
		cancel()
		if ctx.Err() != nil {
			return closeFailure(ctx.Err())
		}
		current, e := w.Store.Get(ctx, child.ID)
		if e != nil {
			return closeFailure(e)
		}
		if !storage.Terminal(current.Status) {
			status, code := "completed", ""
			if runErr != nil {
				status, code = "failed", "EXECUTION_FAILED"
				var failure JobFailure
				if errors.As(runErr, &failure) {
					status, code = failure.JobTerminalStatus(), failure.Code
				}
				if errors.Is(runErr, context.DeadlineExceeded) {
					code = "JOB_DEADLINE_EXCEEDED"
				}
				if errors.Is(runErr, context.Canceled) {
					status, code = "interrupted", "JOB_INTERRUPTED"
				}
				if current.ResultRef != "" && status == "failed" {
					status = "partial"
				}
			}
			if e = w.Store.FinishWithCode(ctx, child.ID, status, code); e != nil {
				return closeFailure(e)
			}
		}
		batch, e := w.Store.GetBatchRequest(ctx, parent.ID)
		if e != nil {
			return closeFailure(e)
		}
		ordinal := 0
		for _, segment := range batch.Segments {
			if segment.ChildJobID == child.ID {
				ordinal = segment.Ordinal
				break
			}
		}
		if ordinal == 0 {
			return closeFailure(JobFailure{Code: "BATCH_PLAN_INVALID"})
		}
		segment, e := w.Store.RefreshBatchSegment(ctx, parent.ID, ordinal)
		if e != nil {
			return closeFailure(e)
		}
		if permanentBatchFailure(segment.ErrorCode) {
			break
		}
	}
	batch, e := w.Store.GetBatchRequest(ctx, parent.ID)
	if e != nil {
		return closeFailure(e)
	}
	status, code := "completed", ""
	for _, segment := range batch.Segments {
		if segment.Status != "completed" || segment.Manifest == nil || segment.AuditRef == "" || segment.ReportRef == "" {
			status = "partial"
			if code == "" {
				code = segment.ErrorCode
			}
		}
	}
	if status != "completed" {
		if batch.ErrorCode != "" {
			code = batch.ErrorCode
		}
		if code == "" {
			code = "BATCH_PARTIAL"
		}
		if len(batch.Covered) == 0 {
			status = "failed"
		}
	}
	if !storage.Terminal(batch.Status) {
		if e = w.Store.StopBatch(ctx, parent.ID, status, code); e != nil {
			return closeFailure(e)
		}
	}
	if e = w.Store.FinishWithCode(ctx, parent.ID, status, code); e != nil {
		return closeFailure(e)
	}
	if status != "completed" {
		return JobFailure{Code: code, Partial: status == "partial"}
	}
	return nil
}

func permanentBatchFailure(code string) bool {
	switch code {
	case "CLOUD_ACCOUNT_MISMATCH", "CLOUD_OWNER_MISMATCH", "CLOUD_GATEWAY_MISMATCH", "CLOUD_ASSET_SCOPE_MISMATCH", "CLOUD_GROUP_SCOPE_MISMATCH", "COLLECTOR_SCOPE_DENIED", "PROVIDER_IDENTITY_CONFLICT", "PROVIDER_RESPONSE_INVALID", "CLOUD_RESPONSE_INVALID", "UPSTREAM_RESPONSE_TOO_LARGE", "CLOUD_IDENTITY_INVALID", "CLOUD_IDENTITY_REFUSED", "CLOUD_IDENTITY_FAILED", "CLOUD_IDENTITY_NOT_VERIFIED", "CLOUD_CREDENTIAL_EXPIRED", "CLOUD_CREDENTIAL_UNAVAILABLE", "CLOUD_CREDENTIAL_MODE_UNSUPPORTED", "CLOUD_CONFIGURATION_INVALID", "CLOUD_CONFIGURATION_FAILED", "CLOUD_NOT_CONFIGURED", "CLOUD_PLAN_INVALID", "CLOUD_QUERY_REFUSED", "CLOUD_ACCESS_DENIED", "CLOUD_RESPONSE_CONTAINS_CREDENTIAL", "CONFIGURATION_STALE", "BUDGET_EXPIRED", "BUDGET_NOT_CONFIGURED", "EXECUTION_CLOSED", "EFFECT_DENIED", "ATTEMPT_LIMIT", "MODEL_CONFIGURATION_INVALID", "PREPARATION_PLAN_INVALID", "PREPARATION_SCOPE_DENIED", "INPUT_BIND_FAILED", "AGENT_RESULT_SAVE_FAILED", "RESULT_SAVE_FAILED", "REPORT_SAVE_FAILED", "COLLECTION_OBSERVATION_SAVE_FAILED":
		return true
	}
	return false
}
