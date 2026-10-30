package app

import (
	"context"
	"errors"
	"time"

	"cfw-policy-llm-agent/server/internal/storage"
)

// Guidance reads only committed local state. Browser supplied state flags are
// never an authority for a suggested effect or a confirmation.
func (s *IntentService) Guidance(ctx context.Context, conversation, jobID, reportID, surface string) (GuidanceCatalog, error) {
	g := GuidanceContext{Surface: surface, Mode: "rules_only", CloudReady: s.Plans.Cloud != nil && s.Plans.Cloud.Ready, ModelReady: s.Plans.ModelReady}
	if surface != "" && !hasString([]string{"conversation", "overview", "profile", "audit", "finding", "report"}, surface) {
		return GuidanceCatalog{}, storage.ErrInputInvalid
	}
	// Report and task surfaces may omit the conversation parameter. Resolve it
	// from their committed producer, never from the browser's current workspace.
	if conversation == "" {
		if jobID != "" {
			job, e := s.Store.Get(ctx, jobID)
			if e != nil {
				return GuidanceCatalog{}, e
			}
			conversation = job.ConversationID
		} else if reportID != "" {
			r, e := s.Store.GetReport(ctx, reportID)
			if e != nil {
				return GuidanceCatalog{}, e
			}
			producer, found, e := s.Store.JobForAudit(ctx, r.AuditID)
			if e != nil {
				return GuidanceCatalog{}, e
			}
			if found {
				conversation = producer.ConversationID
			}
		}
	}
	if conversation != "" {
		if _, e := s.Store.GetConversation(ctx, conversation); e != nil {
			return GuidanceCatalog{}, e
		}
		in, e := s.Current(ctx, conversation)
		if e != nil {
			return GuidanceCatalog{}, e
		}
		g.Refs.ConversationID = conversation
		g.Refs.IntentID = in.ID
		g.Refs.IntentRevision = in.Revision
		g.IntentStatus, g.Mode, g.DataPolicy, g.Boundary, g.Direction = in.Status, in.Mode, in.DataPolicy.Mode, in.Boundary, in.Direction
		g.ManualChange = in.DataPolicy.Mode == "force_refresh" && in.Action == "recheck" && in.Status != "ready_for_confirmation"
		g.FreshAfter = in.DataPolicy.FreshAfter
		if in.Action == "explain" && in.ContextReportID != "" && reportID == "" {
			reportID = in.ContextReportID
			g.Historical = true
		}

		if in.Proposal != nil {
			g.Refs.ProposalID, g.Refs.PlanID = in.Proposal.ID, in.Proposal.PlanID
			g.ProposalStatus = in.Proposal.Status
			if in.Proposal.PlanID != "" {
				p, e := s.Store.GetPlan(ctx, in.Proposal.PlanID)
				if e != nil {
					return GuidanceCatalog{}, e
				}
				at, e := time.Parse(time.RFC3339Nano, p.ExpiresAt)
				if e != nil {
					return GuidanceCatalog{}, storage.ErrInputInvalid
				}
				if p.ConsumedJobID == "" && !time.Now().Before(at) {
					g.ProposalStatus = "expired"
				}
				if jobID == "" && p.ConsumedJobID != "" && (reportID == "" || p.Kind == "assistant_turn" && p.ContextReportID == reportID) {
					jobID = p.ConsumedJobID
				}

			}
		}
		if jobID == "" && reportID == "" && in.ID == "" {
			jobs, e := s.Store.ListJobs(ctx, conversation, 0, 1)
			if e != nil {
				return GuidanceCatalog{}, e
			}
			if len(jobs) > 0 {
				jobID = jobs[0].ID
			}
		}
	}
	if reportID != "" {
		report, e := s.Store.GetReport(ctx, reportID)
		if e != nil {
			return GuidanceCatalog{}, e
		}
		audit, e := s.Store.GetAudit(ctx, report.AuditID)
		if e != nil {
			return GuidanceCatalog{}, e
		}
		producer, found, e := s.Store.JobForAudit(ctx, audit.ID)
		if e != nil {
			return GuidanceCatalog{}, e
		}
		if found && conversation != "" && producer.ConversationID != conversation {
			return GuidanceCatalog{}, ErrScopeDenied
		}
		if jobID != "" && (!found || jobID != producer.ID) {
			selectedJob, e := s.Store.Get(ctx, jobID)
			if e != nil {
				return GuidanceCatalog{}, e
			}
			selectedPlan, e := s.Store.GetPlan(ctx, selectedJob.PlanID)
			if e != nil {
				return GuidanceCatalog{}, e
			}
			if selectedJob.Kind != "assistant_turn" || selectedPlan.ContextReportID != reportID || selectedJob.ConversationID != producer.ConversationID {
				return GuidanceCatalog{}, ErrScopeDenied
			}
		}
		if conversation != "" && !found {
			return GuidanceCatalog{}, ErrScopeDenied
		}
		if found {
			if jobID == "" {
				jobID = producer.ID
			}
			g.Refs.ConversationID = producer.ConversationID
		}
		g.Refs.ReportID, g.Refs.SnapshotID = report.ID, audit.SnapshotID
		g.Historical, g.RulesAvailable, g.FactsAvailable = true, true, true
		g.ManualChange = false
		g.Boundary, g.Direction = audit.Input.Scope.Boundary, audit.Input.Scope.Direction
	}
	if jobID != "" {
		view, e := s.Store.ReadJobWorkspace(ctx, jobID)
		if e != nil {
			return GuidanceCatalog{}, e
		}
		if conversation != "" && view.Job.ConversationID != conversation {
			return GuidanceCatalog{}, ErrScopeDenied
		}
		g.Refs.ConversationID, g.Refs.JobID, g.Refs.SnapshotID = view.Job.ConversationID, view.Job.ID, view.SnapshotID
		g.JobStatus = view.Job.Status
		g.Batch = view.Batch != nil
		g.FactsAvailable, g.RulesAvailable = g.FactsAvailable || view.InputSealed, g.RulesAvailable || view.Audit != nil
		if view.ReportPublished && g.Refs.ReportID == "" {
			g.Refs.ReportID = view.ReportRef
		}
		if view.Audit != nil {
			g.DataIncomplete = !view.Audit.Result.Coverage.Complete
			for _, a := range view.Audit.Input.Assets {
				if len(a.Services) == 0 {
					g.BusinessMissing = true
				}
				for _, sv := range a.Services {
					if sv.RoleStatus != "confirmed" || !sv.AllowedConfirmed || !sv.RequiredConfirmed {
						g.BusinessMissing = true
					}
				}
			}
		}
		if view.AgentResult != nil {
			g.HasSavedExplanation = view.AgentResult.Content.Output != nil
		}
		// Failure to seal is an ordinary preparation gap, not a provider fact.
		if !view.InputSealed && storage.Terminal(view.Job.Status) {
			g.DataIncomplete = true
		}
	}
	if conversation != "" && !g.Historical {
		business, e := s.Store.ConversationBusinessContext(ctx, conversation)
		if e == nil {
			if (jobID == "" || business.Target.JobID == jobID) && business.Draft != nil {
				g.Refs.DraftID = business.Draft.ID
				if g.Refs.SnapshotID == "" {
					g.Refs.SnapshotID = business.Target.SnapshotID
				}
				g.BusinessMissing = business.Draft.Status != "confirmed"
			}
		} else if !errors.Is(e, storage.ErrBusinessTargetUnresolved) {
			return GuidanceCatalog{}, e
		}
	}
	return BuildGuidance(g), nil
}
