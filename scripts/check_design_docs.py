#!/usr/bin/env python3
"""Offline documentation checks only; never runs a rule engine, SDK, model or API."""
from __future__ import annotations

import json
import re
from datetime import datetime
from pathlib import Path
from urllib.parse import unquote, urlparse

ROOT = Path(__file__).resolve().parents[1]
DESIGN_DOCS = {p.name for p in (ROOT / "docs/design").glob("*.md")}


def require(value: bool, message: str) -> None:
    if not value:
        raise ValueError(message)


def check_markdown() -> tuple[int, int]:
    links = json_blocks = 0
    paths = [ROOT / "README.md", ROOT / "AGENTS.md", *(ROOT / "docs").rglob("*.md"), *(ROOT / "experiments").rglob("*.md")]
    for path in paths:
        text = path.read_text(encoding="utf-8")
        prose: list[str] = []
        fence = ""
        for line in text.splitlines():
            marker = re.match(r"^\s*(`{3,}|~{3,})", line)
            if marker:
                token = marker.group(1)
                if not fence:
                    fence = token
                elif token[0] == fence[0] and len(token) >= len(fence):
                    fence = ""
                continue
            if not fence:
                prose.append(line)
        require(not fence, f"unclosed code fence: {path.relative_to(ROOT)}")
        for target in re.findall(r"!?\[[^\]]*\]\(([^)]+)\)", "\n".join(prose)):
            target = target.strip().split(' "', 1)[0]
            if urlparse(target).scheme or target.startswith("#"):
                continue
            target = unquote(target.split("#", 1)[0])
            if target:
                links += 1
                require((path.parent / target).exists(), f"missing link: {path.relative_to(ROOT)} -> {target}")
        if path.parent == ROOT / "docs/design" and path.name in DESIGN_DOCS:
            require("G2" in text, f"missing G2 status: {path.name}")
            for block in re.findall(r"```json\s*\n(.*?)\n```", text, re.S):
                json.loads(block)
                json_blocks += 1
    return links, json_blocks


def index(items: list[dict], label: str) -> dict[str, dict]:
    result = {item["id"]: item for item in items}
    require(len(result) == len(items), f"duplicate {label} id")
    return result


def check_examples() -> None:
    data = json.loads((ROOT / "docs/validation/fixtures/audit-contracts.json").read_text(encoding="utf-8"))
    require(data["documentation_only"] and data["source_kind"] == "synthetic", "examples must stay synthetic")
    require(not data["formal_business_implementation"] and not data["semantics_evaluated"], "no business-validation claim")
    require(data["design_version"] == "0.6" and data["requirement_baseline"] == "B2", "design/baseline mismatch")
    limits = data["limits"]
    require(sum(limits["phases_seconds"].values()) == limits["execution_seconds"] == 240, "phase budget mismatch")
    require(limits["queue_seconds"] + limits["execution_seconds"] == limits["total_seconds"] == 360, "root budget mismatch")
    require(limits["cloud_http_attempts"] == 100 and limits["model_http_attempts"] == 6, "changed design caps")
    require(limits["nat_gateways_per_task"] == 1 and limits["collection_sweeps_max"] == 2, "scope/sweep mismatch")
    scopes = index(data["policy_scopes"], "scope")
    valid_pairs = {("internet", "in"), ("internet", "out"), ("nat", "out")}
    canonical_keys = set()
    for scope in scopes.values():
        key = scope["key"]
        require((key["boundary"], key["direction"]) in valid_pairs and key["ip_family"] == "ipv4", "unsupported scope")
        canonical_keys.add(json.dumps(key, sort_keys=True))
    require(len(canonical_keys) == len(scopes), "canonical scope collision")
    sections = index(data["snapshot_sections"], "section")
    snapshots = index(data["snapshots"], "snapshot")
    for section in sections.values():
        require(section["policy_scope_ref"] in scopes and section["source_kind"] == "synthetic", "section ownership/source")
        require(section["config_sweeps"] <= limits["collection_sweeps_max"], "sweep cap")
        require(datetime.fromisoformat(section["started_at"]) <= datetime.fromisoformat(section["finished_at"]), "section time")
    for snapshot in snapshots.values():
        require(bool(snapshot["section_refs"]), "empty snapshot sections")
        require(all(sections[ref]["policy_scope_ref"] == snapshot["policy_scope_ref"] for ref in snapshot["section_refs"]), "cross-scope snapshot")
    requests = index(data["plan_requests"], "request")
    plans = index(data["execution_plans"], "plan")
    for plan in plans.values():
        request = requests[plan["request_id"]]
        scope = scopes[request["policy_scope_ref"]]
        require(plan["policy_scope_ref"] == request["policy_scope_ref"], "plan scope changed")
        require(plan["connection_revision"] == scope["connection_revision"], "connection revision mismatch")
        require(not plan["is_executed"], "design plan is not an execution")
        ttl = datetime.fromisoformat(plan["expires_at"]) - datetime.fromisoformat(plan["created_at"])
        require(ttl.total_seconds() == limits["plan_ttl_seconds"], "plan TTL mismatch")
        selection = request["selection"]
        if selection["kind"] == "assets":
            require(1 <= len(selection["asset_ids"]) <= limits["assets_per_task"] and len(set(selection["asset_ids"])) == len(selection["asset_ids"]), "asset selection must be explicit and unique")
        else:
            require(selection["kind"] == "gateway_policy" and scope["key"]["boundary"] == "nat", "gateway mode scope")
            require(selection["gateway_ref"] == scope["key"]["policy_set_ref"], "wrong gateway")
        if request["data_policy"]["mode"] == "pinned_snapshot":
            require(snapshots[request["data_policy"]["snapshot_id"]]["policy_scope_ref"] == plan["policy_scope_ref"], "pinned scope mismatch")
            require(not plan["effect_ceiling"]["allow_cloud_reads"] and plan["limits"]["cloud_http_attempts"] == 0, "pinned must not read cloud")
        if request["mode"] == "rules_only":
            require(not plan["effect_ceiling"]["allow_model_requests"] and plan["limits"]["model_http_attempts"] == 0, "rules-only must not call model")
        for cap in ("cloud_http_attempts", "model_http_attempts", "model_tool_calls"):
            require(plan["limits"][cap] <= limits[cap], f"plan exceeds {cap}")
    for start in data["start_requests"]:
        require(start["body"]["plan_id"] in plans, "start must use plan")
        require(set(start) == {"headers", "body"}, "unknown start envelope")
        require(set(start["body"]) == {"plan_id"}, "start must not override kind/scope/budget")
        require(set(start["headers"]) == {"Idempotency-Key"}, "expected idempotency header")
    for manifest in data["resolved_input_manifests"]:
        plan = plans[manifest["plan_id"]]
        snapshot = snapshots[manifest["snapshot_id"]]
        require(manifest["policy_scope_ref"] == plan["policy_scope_ref"] == snapshot["policy_scope_ref"], "sealed scope mismatch")
        require(all(ref in snapshot["section_refs"] for ref in manifest["section_refs"]), "sealed section outside snapshot")
    identities = [(p["snapshot_id"], p["policy_scope_ref"], p["provider_acl_id"]) for p in data["identity_examples"]]
    require(len(set(identities)) == len(identities), "ACL identity must include partition")
    association = data["nat_association_example"]
    require(not association["can_claim_actual_path"] and not association["can_run_asset_business_comparison"], "candidate relation is not a path")
    require(not data["semantic_example"]["evaluated_by_this_repository"], "static example must not claim evaluation")
    messages = index(data["conversation_messages_example"], "message")
    for context in data["model_context_examples"]:
        scope = plans[context["plan_id"]]["policy_scope_ref"]
        require(context["not_an_actual_model_request"], "context is an example, not a model request")
        require(context["policy_scope_ref"] == context["summary_policy_scope_ref"] == scope, "cross-scope summary")
        require(all(messages[ref]["policy_scope_ref"] == scope for ref in context["message_refs"]), "cross-scope message disclosure")
        require(not set(context["message_refs"]) & set(context["excluded_message_refs"]), "excluded message included")
    policies = {p["kind"]: p for p in data["admission_policy_examples"]}
    require(set(policies) == {"audit", "collect", "connection_check", "assistant_turn"}, "missing job admission")
    require(all(p["all_requests_use_admission"] for p in policies.values()), "admission bypass")
    require(policies["assistant_turn"]["cloud_attempt_limit"] == 0, "assistant must not refresh cloud")
    require(policies["assistant_turn"]["model_use_accepted_required"], "assistant requires explicit send")
    require(all(policies[k]["model_attempt_limit"] == 0 for k in ("collect", "connection_check")), "non-model job calls model")
    require(policies["connection_check"]["cloud_attempt_limit"] == 6 and not policies["connection_check"]["proves_all_api_permissions"], "connection check is bounded, not full validation")
    review = data["review_persistence_example"]
    require(review["documentation_only"] and not review["storage_failure"]["saved"], "failed review cannot be saved")
    require(review["storage_failure"]["keep_draft"] and review["storage_failure"]["retry_same_key"], "failed review recovery")
    require(review["finding_verdict_before"] == review["finding_verdict_after"], "review changed finding")
    require(review["response_after_commit"]["review_revision"] == review["initial_review_revision"]+1, "review revision")
    require(review["replay_idempotency_before_revision_check"], "review idempotency replay order")
    for basis in data["basis_contract_examples"]:
        require(basis["illustrative_only"], "basis must be illustrative")
        if basis["input_source_kind"] == "imported":
            require(basis["expected_basis"] == "imported_assumption", "import promoted to live assurance")
            require(basis["qualification_required"] and not basis["claims_real_cloud_safety"], "import qualification missing")
        if basis["known_gap"] or not basis["references_closed"] or not basis["semantics_supported"] or not basis["scope_consistent"] or not basis["declaration_present"]:
            require(basis["expected_verdict"] in {"insufficient_data", "unsupported"}, "known gap must not pass")
    target_kinds = {"policy_change": {"existing_policy", "policy_set"}, "coverage_followup": {"protection_scope", "asset_service"}, "component_consideration": {"asset_service", "scope"}, "confirm_information": {"business_intent", "asset_service", "scope"}}
    for rec in data["recommendation_contract_examples"]:
        require(rec["target_kind"] in target_kinds[rec["kind"]], "recommendation target kind mismatch")
        require(rec["policy_scope_ref"] in scopes and rec["evidence_refs"] and rec["conditions"], "recommendation evidence/scope")
        require(not rec["has_cloud_write_action"], "recommendation must not execute cloud write")
    evaluation = data["evaluation_design"]
    require(evaluation["documentation_only"] and not evaluation["formal_dataset_created"] and evaluation["formal_runs_executed"] == 0, "no invented evaluation")
    require((evaluation["min_independent_cases"], evaluation["max_independent_cases"]) == (20,30), "case count drift")
    require(not evaluation["resource_count_is_case_count"] and evaluation["prefer_single_ecs_serial_reuse"], "VM count must not equal case count")
    require(evaluation["minimum_real_rechecks"] == 1 and evaluation["actual_real_rechecks"] == 0, "real recheck requirement/claim")
    require(not evaluation["system_cloud_writes"] and evaluation["cleanup_actor"] == "human_operator", "cleanup is not a system write")
    arms = {arm["id"]: arm for arm in evaluation["arms"]}
    require(set(arms) == {"rules_only", "fixed_model_pipeline", "agent_tool_selection"}, "three experiment arms")
    require(not arms["rules_only"]["uses_model"] and arms["rules_only"]["model_http_calls"] == 0, "rules-only uses model")
    require(all(a["input_ref"] == evaluation["shared_input_ref"] and a["rules_ref"] == evaluation["shared_rule_result_ref"] for a in arms.values()), "unshared baseline")
    for key in ("model_config_ref","sampling_ref","initial_information_ref","tools_ref","projection_ref","budget_ref"):
        require(arms["fixed_model_pipeline"][key] == arms["agent_tool_selection"][key], "unfair model-group condition: "+key)
    require(set(evaluation["separate_output_metrics"]) == {"rule_facts","model_hypotheses","recommendations"}, "mixed evaluation outputs")
    require(evaluation["run_status_denominator"] == "all_formal_runs" and evaluation["zero_denominator"] == "not_applicable", "evaluation denominator")
    require(not evaluation["labels_visible_to_model"], "label leakage")
    require(not data["model_capability_design"]["ordinary_text_success_implies_tool_compatibility"], "text-only capability promotion")
    lifecycle = data["terminal_lifecycle_design"]
    require(lifecycle["documentation_only"] and lifecycle["queued_cancel_is_immediate"], "queued cancel contract")
    require(lifecycle["running_cancel_response_state"] == "cancelling", "running cancellation acknowledgment")
    require(not lifecycle["late_success_can_overwrite_terminal"] and not lifecycle["restart_replays_calls"], "terminal/restart safety")
    require(lifecycle["exclusive_lock_before_recovery"] and lifecycle["precount_before_physical_attempt"], "recovery/request ordering")
    perf = data["performance_design"]
    require(perf["documentation_only"] and not perf["measured_by_this_review"], "no invented performance result")
    require(not perf["requires_new_cloud_ecs"] and not perf["truncation_counts_as_performance_pass"], "performance scope/claim")
    require(perf["stress_rule_stage_deadline_seconds"] <= limits["phases_seconds"]["rules"], "performance exceeds rule-stage budget")
    matrix = (ROOT / "docs/validation/verification-plan.md").read_text(encoding="utf-8")
    for case in data["trace_matrix_case_ids"]:
        require(f"| {case} |" in matrix, f"missing planned case {case}")
    require(len(data["trace_matrix_case_ids"]) == 18, "unexpected trace matrix size")


def scoped_document(relative_path: str) -> Path:
    candidate = (ROOT / relative_path).resolve()
    require(not Path(relative_path).is_absolute() and candidate.is_relative_to(ROOT.resolve()), "document path outside workspace")
    require(candidate.is_relative_to((ROOT / "docs").resolve()) and candidate.suffix == ".md", "trace must reference a design/requirement document")
    return candidate




def check_governance() -> None:
    """Check current layout and document contracts; history belongs in Git."""
    require([p.name for p in (ROOT / "docs").glob("*.md")] == ["README.md"], "docs root must be navigation only")
    trace = json.loads((ROOT / "docs/validation/requirement-traceability.json").read_text(encoding="utf-8"))
    require(trace["approval_status"]["requirements"] == "G1_passed" and trace["approval_status"]["system_design"] == "G2_passed", "approval state drift")
    supplement = json.loads((ROOT / "docs/validation/fixtures/concurrency-and-api.json").read_text(encoding="utf-8"))
    require(supplement["documentation_only"] and supplement["design_version"] == "0.6", "supplement scope")
    require(not any(supplement[k] for k in ("formal_business_implementation", "formal_concurrency_tested", "formal_api_generated", "real_cloud_or_model_called")), "invented runtime evidence")
    require(supplement["execution"]["worker_slots"] == 3 and supplement["execution"]["per_conversation_unfinished"] == 1, "small concurrency drift")
    sqlite = supplement["sqlite"]
    require(sqlite["journal_mode"] == "WAL" and sqlite["pooling"] == "single_shared_database_sql_pool", "simple SQLite baseline drift")
    require(not any(sqlite[k] for k in ("force_single_writer_connection", "force_separate_read_write_pools", "custom_write_queue", "custom_checkpoint_scheduler", "tx_across_network")), "SQLite over-design or transaction network wait")
    api = supplement["api_docs"]
    require(api["generator"] == "swaggo/swag" and api["artifacts_tracked_after_generation"] and not api["generation_verified"], "API documentation handoff drift")
    matrix = (ROOT / "docs/validation/verification-plan.md").read_text(encoding="utf-8")
    require(supplement["planned_check_ids"] == [f"V6-{i:02d}" for i in range(1,9)], "supplement checks")
    require(all(f"| {case} |" in matrix for case in supplement["planned_check_ids"]), "missing supplement planned check")
    return None


def check_traceability() -> int:
    trace = json.loads((ROOT / "docs/validation/requirement-traceability.json").read_text(encoding="utf-8"))
    require(trace["design_version"] == "0.6" and trace["requirement_baseline"] == "B2", "trace baseline")
    require(not trace["formal_business_tests_executed"], "trace is not a business-test result")
    text = (ROOT / "docs/requirements/system.md").read_text(encoding="utf-8")
    definitions = set(re.findall(r"^\| ((?:FR|NFR|AC)-\d{2})\b", text, re.M))
    cr = set(re.findall(r"^- \*\*(CR-1-F\d{2})\b", text, re.M))
    expected = {f"AC-{i:02d}" for i in range(1,22)} | cr
    entries = index(trace["entries"], "trace")
    require(set(entries) == expected and len(cr) == 4, "incomplete acceptance/scope trace")
    covered = set()
    matrices = (ROOT/"docs/validation/verification-plan.md").read_text(encoding="utf-8") + (ROOT/"docs/validation/verification-plan.md").read_text(encoding="utf-8")
    for entry in entries.values():
        require(not entry["implementation_verified"], "trace must not claim implementation")
        require(set(entry["requirement_ids"]) <= definitions, "undefined requirement ID")
        covered.update(entry["requirement_ids"])
        require(entry["design_evidence"] and entry["planned_checks"], "trace needs design and checks")
        for evidence in entry["design_evidence"]:
            require(evidence["marker"] in scoped_document(evidence["file"]).read_text(encoding="utf-8"), "missing design evidence: "+entry["id"]+" / "+evidence["marker"])
        for case in entry["planned_checks"]:
            require(f"| {case} |" in matrices, "undefined planned check: "+case)
    require({x for x in definitions if x.startswith(("FR-","NFR-"))} <= covered, "unmapped FR/NFR")
    require(entries["AC-13"]["status"] == "optional_disabled_by_default", "M1 must remain optional")
    return len(entries)



def check_transport_contracts(data: dict | None = None) -> int:
    """Checks documentation fixture consistency only, NOT a network/SSE implementation."""
    if data is None:
        data = json.loads((ROOT / "docs/validation/fixtures/frontend-events.json").read_text(encoding="utf-8"))
    require(data["documentation_only"] and data["source_kind"] == "synthetic", "transport fixtures must stay synthetic")
    require(data["design_version"] == "0.6" and data["requirement_baseline"] == "B2", "transport design baseline")
    require(not data["formal_transport_tested"] and not data["model_streaming_verified"], "no invented transport/model test")
    ui = data["frontend"]
    require((ui["framework"], ui["bundler"], ui["router"], ui["ui"], ui["styles"]) ==
            ("vue", "vite", "vue-router", "nuxt-ui-v4", "tailwind-v4"), "frontend direction drift")
    require(not ui["nuxt_application_framework"] and not ui["runtime_remote_assets"] and not ui["formal_build_verified"], "frontend scope/claim")
    t = data["transport"]
    require((t["primary"], t["fallback"], t["application_auth"], t["cursor_scope"], t["cursor_field"]) ==
            ("fetch_sse", "json_polling", "request_header_only", "job", "after_seq"), "transport/auth contract")
    for field in ("commit_before_publish", "state_event_atomic", "terminal_drain_required"):
        require(t[field] is True, "missing transport ordering: " + field)
    for field in ("disconnect_cancels_job", "reconnect_creates_job", "reconnect_replays_external_calls",
                  "persist_cursor_without_view", "hold_db_during_network_wait", "heartbeat_persisted",
                  "raw_reasoning_exposed", "upstream_model_streaming", "poll_and_stream_simultaneous",
                  "auth_error_auto_retries", "history_pruning"):
        require(t[field] is False, "unsafe transport boundary: " + field)
    require(t["unknown_schema"] == "stop" and t["gap_action"] == "backfill_before_advance", "protocol error must not skip")
    expected_limits = {"max_subscriptions": 8, "page_limit": 100, "event_bytes": 4096,
                       "parse_buffer_bytes": 65536, "heartbeat_seconds": 15, "read_idle_seconds": 45,
                       "write_seconds": 10, "subscription_seconds": 120, "network_failures_before_polling": 3}
    require(all(t[k] == v for k,v in expected_limits.items()), "transport engineering defaults drift")
    events = data["events"]
    require(bool(events), "empty transport fixture")
    types = {"job_status", "stage_changed", "tool_started", "tool_finished", "findings_available",
             "message_available", "report_available", "job_terminal"}
    job_id = events[0]["job_id"]
    for i, event in enumerate(events, 1):
        require(type(event["seq"]) is int and event["seq"] == i, "event sequence must be contiguous")
        require(event["job_id"] == job_id and event["schema_version"] == 1, "event owner/schema mismatch")
        require(event["type"] in types and event["payload"]["phase"] in {"L0","L1","L2","L3","L4"}, "event type/phase")
        require(len(json.dumps(event, ensure_ascii=False).encode()) <= t["event_bytes"], "event payload oversize")
        require(datetime.fromisoformat(event["occurred_at"]).tzinfo is not None, "event timestamp missing timezone")
        if event["type"] == "job_terminal":
            require(i == len(events), "terminal must be last")
        if event["type"] == "message_available":
            require(event["payload"]["complete_message"] is True and event["payload"]["message_ref"], "nonstream message reference")
    require(events[-1]["type"] == "job_terminal", "missing terminal event")
    require(events[-1]["payload"]["status"] in {"completed","partial","failed","cancelled","interrupted"}, "invalid terminal state")
    replay = data["reconnect"]
    require(replay["replay_seq"] == [e["seq"] for e in events if e["seq"] > replay["after_seq"]], "incorrect replay suffix")
    require(replay["external_http_attempts"] == 0, "reconnect must not invoke external services")
    duplicate = data["duplicate_delivery"]
    require(list(dict.fromkeys(duplicate["received"])) == duplicate["applied"] == [e["seq"] for e in events], "duplicate fixture projection mismatch")
    terminal = data["terminal_empty_stream"]
    require(terminal["job_status"] == events[-1]["payload"]["status"] and
            terminal["after_seq"] == terminal["last_seq"] == events[-1]["seq"] and terminal["http_status"] == 204, "invalid terminal 204 example")
    matrix = (ROOT/"docs/validation/verification-plan.md").read_text(encoding="utf-8")
    require(data["planned_check_ids"] == [f"V5-{i:02d}" for i in range(1,9)], "missing planned transport checks")
    for case in data["planned_check_ids"]:
        require(f"| {case} |" in matrix, "unmapped planned transport check: " + case)
    return len(events)


def main() -> int:
    try:
        links, blocks = check_markdown()
        check_examples()
        check_transport_contracts()
        trace_count = check_traceability()
        check_governance()
    except (OSError, ValueError, KeyError, TypeError, AssertionError) as error:
        print(f"FAIL: {error}")
        return 1
    print(f"PASS: {links} local documentation links; balanced fences; {blocks} design JSON blocks; B2/design v0.6 documentation contracts; {trace_count} acceptance/scope mappings; classified current documentation.")
    print("LIMIT: static documentation validation only; no rule semantics, SDK build, browser, database, cloud or model tests executed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
