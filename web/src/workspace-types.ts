import type { Job, JobEvent } from './api.ts'

export type PortRange = { from: number; to: number }
export type Access = { scope_ref: string; protocol: string; addresses: string[]; ports: PortRange[]; applicability: string }
export type Scope = { ref: string; provider: string; account_ref: string; control_plane_ref: string; ip_family: string; boundary: string; direction: string; policy_set_ref: string; gateway_ref?: string }
export type Service = { id: string; protocol: string; ports: PortRange[]; role: string; role_status: string; public_access: string; allowed_confirmed: boolean; allowed_complete: boolean; deny_all: boolean; allowed: Access[]; required_confirmed: boolean; required: Access[] }
export type Asset = { provider_resource_id?: string; id: string; scope_ref: string; address: string; address_observed: boolean; association: { status: string; gateway_ref: string; observation_point: string; observed_at: string; evidence_ref: string }; allowed_confirmed: boolean; allowed_complete: boolean; deny_all: boolean; allowed: Access[]; services: Service[] }
export type Policy = { id: string; scope_ref: string; source: { values: string[]; ref?: string }; destination: { values: string[]; ref?: string }; protocol: string; ports: { values: PortRange[]; ref?: string }; action: string; priority: number; order_known: boolean; enabled: string; conditions?: Record<string, unknown> }
export type AuditInput = {
  schema_version: number; snapshot_version: string; profile_version: string; intent_version: string; normalizer_version: string; kernel_version: string; source_kind: string;
  evaluation_at: string; collected_at: string; scope: Scope;
  provider_semantics: { profile_id: string; version: string; default_action: string; source_ref: string };
  completeness: { declared: boolean; enumeration_complete: boolean; observation_consistency: string; known_gaps: string[] };
  protection: { applicable: boolean; required: boolean; state: string; evidence_ref: string };
  selection: { kind: string; asset_ids: string[] }; assets: Asset[]; policies: Policy[];
  baseline: { version: string; enabled_checks: string[]; risk_ports: { protocol: string; port: number; topic: string }[]; exceptions: unknown[] };
}
export type Snapshot = { id: string; policy_scope_ref: string; source_kind: string; created_at: string; input: AuditInput }
export type Page<T> = { items: T[]; offset: number; limit: number }
export type Conversation = { id: string; title: string; policy_scope_ref: string; revision: number; archived: boolean; created_at: string; updated_at: string }
export type CurrentWorkspace = { id: string; current_conversation_id: string; revision: number }
export type ConversationMessage = { id: string; conversation_id: string; seq: number; role: string; origin?: string; public_text: string; policy_scope_ref: string; job_id?: string; ref_kind?: string; ref_id?: string; created_at: string }
export type MessagePage = { items: ConversationMessage[]; next_seq: number; has_more: boolean; last_seq: number }
export type MessageRequest = { client_message_id: string; policy_scope_ref: string; snapshot_id: string; text: string; model_use_accepted: true }
export type MessageAcceptance = { job: Job; message: ConversationMessage; replayed: boolean }
export type Evidence = { id: string; snapshot_version: string; scope_ref: string; subject_ref: string; policy_ref?: string; field: string; summary: string; observed_at: string; value?: unknown }
export type Finding = { id: string; rule_id: string; subcheck?: string; rule_version: string; origin: string; policy_scope_ref: string; subject_ref: string; service_ref?: string; issue_key: string; verdict: string; assessment_basis: string; assumptions: string[]; severity: string; remediation_priority: string; summary: string; query_scope: { subject_ref: string; service_ref: string; fixed_address: string; protocol: string; addresses: string[]; ports: PortRange[] }; differences: { address_from: string; address_to: string; port_from: number; port_to: number; action: string; policy_ref?: string; reason?: string }[]; evidence_refs: string[]; limitations: string[]; accepted_exception: boolean }
export type FindingRecord = { id: string; audit_id: string; finding_key: string; finding: Finding; evidence?: Evidence[]; snapshot_id?: string; source_kind?: string; review_head?: { review_revision?: number; revision?: number; plan: string }; review_revision?: number; plan?: string }
export type AuditResult = { schema_version: number; input_manifest: { snapshot_version: string; profile_version: string; intent_version: string; baseline_version: string; normalizer_version: string; kernel_version: string; policy_scope: Scope; evaluation_at: string; collected_at: string; source_kind: string }; findings: Finding[]; evidence: Evidence[]; coverage: { complete: boolean; evaluated_queries: number; evaluated_cells: number; limitations: string[] } }
export type AuditRecord = { input?: AuditInput; id: string; snapshot_id: string; created_at: string; result: AuditResult; findings: FindingRecord[] }
export type JobResult = { batch?: BatchRequest | null; preparation_discovery?: PreparationDiscovery | null; snapshot_id?: string; cloud_result?: CloudRecord | null; agent_result?: AgentRecord | null; attempt_usage?: AttemptUsage | null; audit?: AuditRecord | null; recheck?: RecheckRecord | null; is_final: boolean; view_seq: number; completed_phases?: string[] }
export type ReviewRequest = { expected_review_revision: number; kind: 'comment' | 'plan_change'; plan?: 'todo' | 'deferred'; comment?: string; reason?: string }
export type ReviewAction = { action_id: string; finding_id: string; review_revision: number; kind: string; plan: string; comment: string; reason: string; saved_at: string }
export type ReportRecord = { id: string; audit_id: string; job_id?: string; format: 'json' | 'markdown'; created_at: string; reviews: { finding_id: string; finding_key: string; review_revision: number; plan: string }[] }
export type RoleChange = { declared?: boolean; service_id: string; role: string; protocol: string; ports: PortRange[] }
export type ServiceAccessChange = { allowed_declared?: boolean; required_declared?: boolean; service_id: string; public_access: string; allowed_complete: boolean; deny_all: boolean; allowed: Access[]; required_confirmed: boolean; required: Access[] }
export type BusinessException = { authorization_source?: string; check_id: string; subcheck?: string; subject_ref: string; service_ref: string; scope_ref: string; expires_at: string; reason: string; confirmed?: boolean }
export type AssetChange = { asset_id: string; roles: RoleChange[]; access?: { allowed_declared?: boolean; exceptions_declared?: boolean; allowed_complete: boolean; deny_all: boolean; allowed: Access[]; services: ServiceAccessChange[]; exceptions?: BusinessException[] } }
export type AssetHead = { asset_id: string; revision: number; profile_version_id: string; intent_version_id: string }
export type ProfileDraft = { id: string; snapshot_id: string; policy_scope_ref: string; revision: number; status: string; preview_id?: string; confirmation_id?: string; changes: AssetChange[]; base_revisions: AssetHead[]; created_at: string; updated_at: string }
export type ProfileDraftCurrentBase = { draft_id: string; draft_revision: number; snapshot_id: string; policy_scope_ref: string; changes: AssetChange[]; base_revisions: AssetHead[] }
export type DraftRebaseRequest = { expected_draft_revision: number; expected_base_revisions: AssetHead[]; changes: AssetChange[] }
export type ConfirmationPreview = { preview_id: string; draft_id: string; draft_revision: number; base_revisions: AssetHead[]; diff: { asset_id: string; section: string; before: unknown; after: unknown }[]; confirmable_sections: string[]; section_dependencies?: Record<string, string[]>; validation_errors?: { asset_id: string; section: string; code: string }[]; created_at: string }
export type ConfirmationResult = { action_id: string; draft_id: string; status: string; revision: number; heads: AssetHead[]; saved_at: string }
export type AssetView = { asset: Asset; declared_asset: Asset; head: AssetHead; local_confirmed_sections: string[]; source_kind: string }
export type PlanRequest = { kind: 'audit' | 'collect' | 'connection_check'; conversation_id: string; data_source_id: string; policy_scope_ref: string; selection: { kind: 'assets' | 'gateway_policy' | 'identity_only'; asset_ids: string[]; gateway_ref?: string }; data_policy: { mode: 'pinned_snapshot' | 'force_refresh'; snapshot_id?: string; allow_stale_fallback?: boolean }; baseline_version_id: string; mode: 'rules_only' | 'rules_and_agent'; evaluation_at?: string }
export type ExecutionPlan = { id: string; expires_at: string; request?: PlanRequest; effect_ceiling?: { allow_cloud_reads?: boolean; allow_model_requests?: boolean; cloud_http_attempts?: number; model_http_attempts?: number }; gaps?: string[]; warnings?: string[]; [key: string]: unknown }
export type Capabilities = { offline_available?: boolean; worker_slots?: number; m1_enabled?: boolean; model_configured?: boolean; model_mode?: string; model_available?: boolean; models?: { id: string; label: string; available?: boolean; configured?: boolean }[]; [key: string]: unknown }
export type RecheckComparison = { comparable: boolean; reasons: string[]; issues: { issue_key: string; state: string; old_finding_ids: string[]; new_finding_ids: string[] }[] }
export type RecheckRecord = { id: string; old_audit_id: string; new_audit_id: string; created_at: string; comparison: RecheckComparison }
export type { Job, JobEvent }

export type LocalBinding = { kind: string; document: 'input' | 'result'; field: string; asset_ref?: string; service_ref?: string; returned: boolean }
export type AgentRoleCandidate = { asset_ref: string; service_ref?: string; role: string; supporting_evidence: string[]; contradicting_evidence: string[]; unknowns: string[]; questions: string[] }
export type AgentRecommendation = { kind: 'policy_change' | 'coverage_followup' | 'component_consideration' | 'confirm_information'; target_kind: string; target_ref: string; basis_kind: string; evidence_refs: string[]; business_ref?: string; finding_ref?: string; action?: string; access_ref?: string; field?: string; component_topic?: string; conditions: string[]; impact: string; manual_next_step: string }
export type AgentOutput = { scope_ref: string; rule_version: string; source_kind: string; assessment_basis: string; explanation: string; roles: AgentRoleCandidate[]; recommendations: AgentRecommendation[]; questions: string[] }
export type AgentContextCoverage = { included_messages: number; excluded_messages: number; truncated: boolean; disclosure_version: string }
export type AgentContent = { job_id: string; input_manifest: AuditResult['input_manifest']; output?: AgentOutput | null; bindings?: Record<string, LocalBinding>; model_calls: number; tool_calls: number; error_code?: string; context: AgentContextCoverage }
export type AgentRecord = { id: string; job_id: string; created_at: string; content: AgentContent }
export type ReportedModelTokens = { prompt_tokens: number; completion_tokens: number; total_tokens: number }
export type AttemptUsage = { cloud_attempts: number; model_attempts: number; unknown_delivery: number; known_usage_responses: number; unknown_usage_attempts: number; reported_tokens: ReportedModelTokens | null; usage_totals_overflow: boolean }
export type DataSource = { id: string; label: string; kind: string; available: boolean; cloud_reads: boolean }
export type CollectionScope = { scope: Scope; assets: { id: string; region: string }[] }
export type CollectionOptions = { source_ref: string; configured: boolean; available: boolean; identity_verified: false; max_http_attempts: number; scopes: CollectionScope[] }
export type CloudObservation = { round: number; operation: string; pages: number; started_at: string; finished_at: string; enumeration_complete: boolean; semantic_sha256: string }
export type CloudRecord = { id: string; job_id: string; created_at: string; content: { job_id: string; kind: 'collect' | 'connection_check' | 'audit'; policy_scope_ref: string; snapshot_id?: string; identity_verified: boolean; product_ready: boolean; observations: CloudObservation[]; limitations: string[] } }

/** Server-owned planning records; these are independent of a selected snapshot. */
export type ConversationMode = 'rules_only' | 'rules_and_agent'
export type ConversationDataPolicy = { mode: 'prefer_fresh' | 'force_refresh' | 'pinned_snapshot'; snapshot_id?: string; fresh_after?: string }
export type ResourceCandidate = { id: string; identity_kind?: 'provider_metadata' | 'artifact_local'; identity_ref?: string; local_asset_id?: string; resource_id: string; name: string; region: string; resource_type: string; source: string; observed_at: string; group?: string }
export type ConversationProposal = { id: string; intent_revision: number; plan_id?: string; summary: string; gaps: string[]; warnings: string[]; effects: { allow_cloud_reads: boolean; allow_model_requests: boolean }; requires_discovery: boolean; status: string }
export type ConversationIntent = {
  id: string; conversation_id: string; revision: number; status: string; action: string;
  query: { name?: string; resource_id?: string; region?: string; resource_type?: string; group?: string };
  boundary: string; direction: string; selection: ResourceCandidate[]; candidates: ResourceCandidate[];
  catalogue: { complete: boolean; observed_at: string; source: string; limitations: string[] };
  clarification?: string; data_policy: ConversationDataPolicy; mode: ConversationMode; proposal?: ConversationProposal | null;
}
export type IntentTurnRequest = { client_turn_id: string; text: string; group?: ResourceGroupRequest; business?: ConversationBusinessRequest; expected_revision?: number; mode?: ConversationMode; data_policy?: ConversationDataPolicy }
export type IntentChoiceRequest = { intent_id: string; expected_revision: number; candidate_ids: string[] }
export type IntentTurnResponse = { conversation: Conversation; message: ConversationMessage | null; reply: ConversationMessage | null; intent: ConversationIntent | null; proposal?: ConversationProposal | null; job?: Job | null; business?: ConversationBusinessResult | null; group?: ResourceGroupResult | null; replayed: boolean }
export type ProposalAcceptance = { job: Job }

export type PreparationCoverage = { account_ref?: string; selector: { name?: string; resource_id?: string; resource_ids?: string[]; region?: string; regions?: string[]; resource_type?: string }; complete: boolean; observed_at: string; source: string; limitations: string[]; revision: number }
export type PreparationDiscovery = { id: string; job_id: string; created_at: string; resources: ResourceCandidate[]; coverages: PreparationCoverage[]; unverified_resource_refs: string[]; complete: boolean; code?: string }
export type PreparedResource = { resource_id: string; region: string; resource_type: 'ecs'; asset_id?: string }
export type BatchPhaseUsage = { collection_ms: number; rules_ms: number; model_ms: number; finalize_ms: number }
export type BatchUsage = { usage_unknown: boolean; unknown_reasons: string[]; cloud_attempts: number; model_attempts: number; recorded_model_calls: number; recorded_tool_calls: number; execution_ms: number; phases: BatchPhaseUsage }
export type BatchSegment = { ordinal: number; resources: PreparedResource[]; child_job_id?: string; status: string; error_code?: string; started_at?: string; finished_at?: string; snapshot_id?: string; audit_ref?: string; report_ref?: string; input_manifest?: AuditResult['input_manifest']; phases: BatchPhaseUsage; execution_ms: number; phase_source?: string }
export type BatchRequest = { parent_job_id: string; plan_id: string; source_ref: string; scope: Scope; spec: { resources: PreparedResource[]; size: number }; workload: Record<string, unknown>; effects: Record<string, unknown>; accepted_at: string; status: string; error_code?: string; segments: BatchSegment[]; covered: PreparedResource[]; failed: PreparedResource[]; unprocessed: PreparedResource[]; usage: BatchUsage }

export type ConversationBusinessTarget = { ref: string; conversation_id: string; intent_id?: string; intent_revision: number; job_id: string; snapshot_id: string; scope: Scope; asset_ids: string[] }
export type BusinessCandidateEvidence = { document: 'input' | 'result'; field: string }
export type BusinessRoleCandidate = { ref: string; asset_id: string; service_id?: string; role: string; protocol: string; ports: PortRange[]; evidence: BusinessCandidateEvidence[]; contradicting_evidence: BusinessCandidateEvidence[]; unknowns: string[]; questions: string[]; decision: string; conflicts_with_confirmed: boolean }
export type ConversationBusinessContext = { target: ConversationBusinessTarget; draft?: ProfileDraft | null; preview?: ConfirmationPreview | null; candidates: BusinessRoleCandidate[] }
export type ConversationBusinessPatch = { asset_id: string; service_id?: string; role?: Omit<RoleChange, 'service_id'> & { service_id?: string }; allowed?: { complete: boolean; deny_all: boolean; allowed: Access[]; public_access?: string }; required?: Access[]; exceptions?: BusinessException[] }
export type ConversationBusinessRequest = { action: 'read' | 'edit' | 'preview' | 'confirm' | 'reject_draft' | 'accept_candidate' | 'reject_candidate' | 'keep_unknown'; expected_target_ref?: string; expected_draft_revision?: number; preview_id?: string; sections?: string[]; patches?: ConversationBusinessPatch[]; candidate_ref?: string; unknown_fields?: string[] }
export type ConversationBusinessResult = { state: string; reply: string; target: ConversationBusinessTarget | null; draft?: ProfileDraft | null; preview?: ConfirmationPreview | null; confirmation?: ConfirmationResult | null; candidates: BusinessRoleCandidate[]; unknown_fields: string[]; missing_inputs: string[]; replayed: boolean }

export type ResourceGroupMember = { resource_ref: string; identity_ref: string; provider: string; source_ref: string; account_ref: string; region: string; resource_type: string; resource_id?: string; local_asset_id?: string; identity_context?: string; name: string; source: string; observed_at: string }
export type ResourceGroupPreview = { id: string; group_id: string; group_name: string; source_ref: string; account_ref: string; conversation_id: string; intent_id: string; intent_revision: number; base_revision: number; authorization_source: string; members: ResourceGroupMember[]; created_at: string; expires_at: string }
export type ResourceGroupVersion = Omit<ResourceGroupPreview, 'base_revision' | 'expires_at'> & { preview_id: string; revision: number; catalogue_revision: number }
export type ResourceGroupRequest = { action: 'preview' | 'confirm'; group_name?: string; resource_refs?: string[]; authorization_source?: string; expected_revision: number; preview_id?: string }
export type ResourceGroupResult = { state: string; preview?: ResourceGroupPreview | null; version?: ResourceGroupVersion | null; missing: string[]; replayed: boolean }
