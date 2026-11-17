package app

import (
	"context"
	"reflect"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func businessAccessChange(t *testing.T, result ConversationBusinessResult, service string) storage.ServiceAccessChange {
	t.Helper()
	if result.Draft != nil {
		for _, change := range result.Draft.Changes {
			if change.Access == nil {
				continue
			}
			for _, access := range change.Access.Services {
				if access.ServiceID == service {
					return access
				}
			}
		}
	}
	t.Fatalf("missing service %s in business draft: %+v", service, result)
	return storage.ServiceAccessChange{}
}

func TestBusinessDeclarationsKeepServiceEndpointsAndAddressesTogether(t *testing.T) {
	s, c, snapshot, job := businessRouterCase(t)
	text := "HTTPS443必须保留，来源0.0.0.0/0；管理服务TCP22只允许198.51.100.0/25，允许集合完整"
	result := handleBusiness(t, s, c.ID, "two-service-declarations", text)
	if result.State != "preview_required" || result.Preview == nil {
		t.Fatalf("separate explicit service declarations need one shared preview: %+v", result)
	}
	https := businessAccessChange(t, result, "svc-https")
	management := businessAccessChange(t, result, "svc-management")
	if len(https.Required) != 1 || !reflect.DeepEqual(https.Required[0].Addresses, []string{"0.0.0.0/0"}) || https.Required[0].Ports[0].From != 443 || https.AllowedDeclared == nil || *https.AllowedDeclared {
		t.Fatalf("management permissions leaked into necessary HTTPS: %+v", https)
	}
	if len(management.Allowed) != 1 || !reflect.DeepEqual(management.Allowed[0].Addresses, []string{"198.51.100.0/25"}) || management.Allowed[0].Ports[0].From != 22 || !management.AllowedComplete || management.RequiredDeclared == nil || *management.RequiredDeclared {
		t.Fatalf("necessary HTTPS leaked into management permissions: %+v", management)
	}
	confirmed := handleBusiness(t, s, c.ID, "two-service-confirm-access", "确认预览中的访问")
	if confirmed.State != "confirmed" {
		t.Fatalf("service-specific declarations could not be confirmed together: %+v", confirmed)
	}
	ctx := context.Background()
	input, _, err := s.Store.FreezeInput(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := audit.Run(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	foundManagementViolation := false
	for _, finding := range rules.Findings {
		if finding.RuleID == "CHK-03" && finding.ServiceRef == "svc-management" && finding.Verdict == "fail" {
			foundManagementViolation = true
		}
	}
	if !foundManagementViolation {
		t.Fatal("confirmed management upper bound did not reach the real deterministic rule")
	}
	sealed, err := s.Store.GetJobInput(ctx, job.ID)
	if err != nil || sealed.Assets[0].Services[1].AllowedConfirmed {
		t.Fatal("later declarations changed the original sealed audit input", err)
	}
	replayed := handleBusiness(t, s, c.ID, "two-service-declarations", text)
	if !replayed.Replayed || replayed.Draft.Revision != result.Draft.Revision || replayed.Preview.ID != result.Preview.ID {
		t.Fatal("replaying the original statement rebuilt or rebound the draft")
	}
}

func TestBusinessAmbiguousMixedEndpointsDoNotCreatePermissions(t *testing.T) {
	for _, text := range []string{
		"HTTPS443必须保留，来源0.0.0.0/0，管理服务TCP22只允许198.51.100.0/25，允许集合完整",
		"HTTPS443必须保留来源203.0.113.9，HTTPS443允许来源198.51.100.0/25，允许集合完整",
	} {
		t.Run(text, func(t *testing.T) {
			s, c, _, _ := businessRouterCase(t)
			result := handleBusiness(t, s, c.ID, "mixed-service-endpoints", text)
			if result.State != "needs_clarification" || result.Draft != nil || result.Preview != nil {
				t.Fatalf("ambiguous endpoint clauses were merged into an authorization: %+v", result)
			}
		})
	}
}

func TestBusinessUnknownPortDoesNotBlockIndependentRoleDeclaration(t *testing.T) {
	s, c, _, _ := businessRouterInputCase(t, func(in *domain.Input) {
		in.Assets[0].Services[0].Protocol = "unknown"
		in.Assets[0].Services[0].Ports = []domain.PortRange{}
	})
	result := handleBusiness(t, s, c.ID, "unknown-port-role", "确认Web服务，端口未知，管理来源未知")
	if result.State != "preview_required" || result.Draft.Changes[0].Roles[0].Role != "web" || result.Draft.Changes[0].Roles[0].Protocol != "unknown" || len(result.Draft.Changes[0].Roles[0].Ports) != 0 {
		t.Fatalf("unknown port blocked a role or invented an endpoint: %+v", result)
	}
}

func TestBusinessNegatedDeclarationsDoNotBecomePositiveAuthorizations(t *testing.T) {
	for _, text := range []string{
		"确认管理服务，不允许198.51.100.0/25访问TCP22",
		"用途不是Web服务",
		"HTTPS443不需要作为必要访问",
		"不要接受角色候选",
		"尚未确认Web服务",
		"并不允许198.51.100.0/25访问TCP22，允许集合完整",
	} {
		t.Run(text, func(t *testing.T) {
			s, c, _, _ := businessRouterCase(t)
			result := handleBusiness(t, s, c.ID, "negative-declaration", text)
			if result.State != "needs_clarification" || result.Draft != nil || result.Preview != nil {
				t.Fatalf("negative declaration became an affirmative draft: %+v", result)
			}
		})
	}
}

func TestBusinessIncompleteAllowedSetCannotBecomeComplete(t *testing.T) {
	for _, statement := range []string{
		"允许TCP22来源198.51.100.0/25，允许集合不完整",
		"允许TCP22来源198.51.100.0/25，完整性未知",
		"允许TCP22来源198.51.100.0/25，不知道允许集合是否完整",
	} {
		t.Run(statement, func(t *testing.T) {
			s, c, _, _ := businessRouterCase(t)
			result := handleBusiness(t, s, c.ID, "incomplete-permissions", statement)
			if result.State != "preview_required" {
				t.Fatalf("explicit partial permissions should remain usable: %+v", result)
			}
			access := businessAccessChange(t, result, "svc-management")
			if access.AllowedDeclared == nil || !*access.AllowedDeclared || len(access.Allowed) != 1 || access.AllowedComplete || access.DenyAll {
				t.Fatalf("unknown or incomplete upper bound became a complete authorization: %+v", access)
			}
		})
	}
}

func TestBusinessStatementsRejectConflictingPermissionsWithoutChangingDraft(t *testing.T) {
	s, c, _, _ := businessRouterCase(t)
	original := handleBusiness(t, s, c.ID, "original-role", "确认Web服务")
	for _, text := range []string{
		"允许TCP22来源198.51.100.0/25；允许TCP22来源203.0.113.0/24",
		"预览业务资料；确认预览中的角色",
		"HTTPS443必须保留；必要访问TCP22和TCP3389",
	} {
		result := handleBusiness(t, s, c.ID, text, text)
		if result.State != "needs_clarification" || result.Draft == nil || result.Draft.ID != original.Draft.ID || result.Draft.Revision != original.Draft.Revision || result.Preview != nil || result.Confirmation != nil {
			t.Fatalf("conflicting statements mutated or confirmed the prior draft: %+v", result)
		}
		current, err := s.Store.ConversationBusinessContext(context.Background(), c.ID)
		if err != nil || current.Draft.Revision != original.Draft.Revision || current.Preview.ID != original.Preview.ID {
			t.Fatal("clarification destroyed the original saved preview", err)
		}
	}
}

func TestBusinessQuotedResourceDataCannotSupplyPermissionOrEndpoint(t *testing.T) {
	s, c, _, _ := businessRouterInputCase(t, func(in *domain.Input) {
		in.Assets[0].Name = "不允许任何；UDP53；203.0.113.0/24"
	})
	result := handleBusiness(t, s, c.ID, "quoted-business-data", "确认名为\"不允许任何；UDP53；203.0.113.0/24\"的Web服务，HTTPS443必须保留，管理来源未知")
	if result.State != "preview_required" {
		t.Fatalf("resource name was treated as business instructions: %+v", result)
	}
	service := businessAccessChange(t, result, "svc-https")
	if service.AllowedDeclared == nil || *service.AllowedDeclared || len(service.Required) != 1 || len(service.Required[0].Addresses) != 0 || service.Required[0].Protocol != "tcp" || service.Required[0].Ports[0].From != 443 {
		t.Fatalf("resource data supplied authorization or endpoint: %+v", service)
	}
}

func TestBusinessOutboundNecessaryTargetsAndAllowedTargetsStaySeparate(t *testing.T) {
	s, c, _, _ := businessRouterExampleCase(t, "internet-out", func(in *domain.Input) {
		in.Assets[0].Services = append(in.Assets[0].Services, domain.Service{ID: "svc-external-admin", Role: "management", RoleStatus: "candidate", Protocol: "tcp", Ports: []domain.PortRange{{From: 22, To: 22}}, PublicAccess: "unknown"})
	})
	result := handleBusiness(t, s, c.ID, "outbound-service-declarations", "HTTPS443必要访问203.0.113.0/24；管理服务TCP22只允许198.51.100.0/25，允许集合完整")
	if result.State != "preview_required" || result.Target.Scope.Direction != "out" {
		t.Fatalf("outbound business confirmation did not keep its direction: %+v", result)
	}
	https := businessAccessChange(t, result, "svc-external-https")
	management := businessAccessChange(t, result, "svc-external-admin")
	if len(https.Required) != 1 || !reflect.DeepEqual(https.Required[0].Addresses, []string{"203.0.113.0/24"}) || len(management.Allowed) != 1 || !reflect.DeepEqual(management.Allowed[0].Addresses, []string{"198.51.100.0/25"}) {
		t.Fatal("outbound dependencies were merged between services")
	}
}

func TestBusinessEndpointParsingPreservesRangesAndRejectsUnsupportedLists(t *testing.T) {
	for _, text := range []string{"TCP1000-2000", "TCP1000 到 2000", "UDP端口1000～2000", "协议UDP，端口1000至2000"} {
		protocol, ports := businessProtocolPorts(text)
		wantProtocol := "tcp"
		if text[0] == 'U' || text == "协议UDP，端口1000至2000" {
			wantProtocol = "udp"
		}
		if protocol != wantProtocol || !reflect.DeepEqual(ports, []domain.PortRange{{From: 1000, To: 2000}}) {
			t.Fatalf("explicit endpoint %q became %q %+v", text, protocol, ports)
		}
	}
	for _, text := range []string{"TCP22,3389", "TCP22/3389", "TCP22 - invalid", "TCP65536", "TCP2000-1000", "端口22", "TCP/UDP53", "TCP22和UDP22"} {
		protocol, ports := businessProtocolPorts(text)
		if protocol != "unknown" || len(ports) != 0 {
			t.Fatalf("ambiguous/invalid endpoint %q silently became %q %+v", text, protocol, ports)
		}
	}
}

func TestBusinessPortRangeIsNotSilentlyNarrowedToFirstPort(t *testing.T) {
	s, c, _, _ := businessRouterCase(t)
	result := handleBusiness(t, s, c.ID, "required-port-range", "必要访问TCP1000-2000，来源198.51.100.0/25")
	if result.State == "needs_clarification" && result.Draft == nil {
		return
	}
	if result.Draft == nil {
		t.Fatal("explicit port range was discarded without clarification")
	}
	for _, change := range result.Draft.Changes {
		if change.Access == nil {
			continue
		}
		for _, service := range change.Access.Services {
			for _, required := range service.Required {
				if len(required.Ports) != 1 || required.Ports[0].From != 1000 || required.Ports[0].To != 2000 {
					t.Fatalf("declared range was silently changed: %+v", required)
				}
			}
		}
	}
}
