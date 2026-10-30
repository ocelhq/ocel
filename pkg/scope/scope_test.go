package scope_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

func servedServices() []protoreflect.ServiceDescriptor {
	var services []protoreflect.ServiceDescriptor
	for _, file := range []protoreflect.FileDescriptor{
		contractv1.File_provider_contract_v1_contract_proto,
		costv1.File_provider_cost_v1_cost_proto,
		variablestorev1.File_provider_variablestore_v1_variablestore_proto,
		runnerv1.File_runner_v1_runner_proto,
		runnerv1.File_runner_v1_jobagent_proto,
		endpointv1.File_endpoint_v1_endpoint_proto,
	} {
		for i := range file.Services().Len() {
			services = append(services, file.Services().Get(i))
		}
	}
	return services
}

func TestEveryMethodAnEndpointServesDeclaresTheScopeItNeeds(t *testing.T) {
	t.Parallel()

	for _, service := range servedServices() {
		for i := range service.Methods().Len() {
			method := service.Methods().Get(i)
			procedure := "/" + string(service.FullName()) + "/" + string(method.Name())
			if _, err := scope.RequiredScope(procedure); err != nil {
				t.Errorf("RequiredScope(%s) = %v, want the scope its method option declares", procedure, err)
			}
		}
	}
}

func TestTheScopeOfAProcedureIsTheOneItsMethodOptionDeclares(t *testing.T) {
	t.Parallel()

	for procedure, want := range map[string]scopev1.Scope{
		variablestorev1connect.VariableStoreServiceRevealValuesProcedure: scopev1.Scope_SCOPE_VARIABLES_REVEAL,
		variablestorev1connect.VariableStoreServiceSetValueProcedure:     scopev1.Scope_SCOPE_VARIABLES_WRITE,
		variablestorev1connect.VariableStoreServiceGetValueProcedure:     scopev1.Scope_SCOPE_VARIABLES_READ,
		"/runner.v1.RunnerService/StartJob":                              scopev1.Scope_SCOPE_JOB,
		"/endpoint.v1.SshCertificateService/IssueUserCertificate":        scopev1.Scope_SCOPE_TARGET_CREDENTIAL,
		"/provider.contract.v1.ProviderService/Rollback":                 scopev1.Scope_SCOPE_APPS_WRITE,
		"/provider.contract.v1.ProviderService/GetProtection":            scopev1.Scope_SCOPE_APPS_READ,
		"/provider.contract.v1.ProviderService/RevealProtectionPassword": scopev1.Scope_SCOPE_APPS_WRITE,
	} {
		got, err := scope.RequiredScope(procedure)
		if err != nil || got != want {
			t.Errorf("RequiredScope(%s) = (%v, %v), want %v", procedure, got, err, want)
		}
	}
}

func TestAProcedureNoServiceDeclaresHasNoScope(t *testing.T) {
	t.Parallel()

	for _, procedure := range []string{
		"/provider.variablestore.v1.VariableStoreService/Sneak",
		"/other.v1.Service/Anything",
		"/v1/heartbeat",
		"/provider.contract.v1.ConfigureRequest/Anything",
		"",
	} {
		if got, err := scope.RequiredScope(procedure); err == nil {
			t.Errorf("RequiredScope(%q) = %v, want it refused", procedure, got)
		}
	}
}

func TestAScopeIsUserGrantableExactlyWhenItHasALeastRole(t *testing.T) {
	t.Parallel()

	values := scopev1.Scope(0).Descriptor().Values()
	for i := range values.Len() {
		declared := scopev1.Scope(values.Get(i).Number())
		hasRole := scope.LeastRole(declared) != scopev1.Role_ROLE_UNSPECIFIED
		if scope.IsUserScope(declared) != hasRole {
			t.Errorf("IsUserScope(%v) = %v, and its least role is %v", declared, scope.IsUserScope(declared), scope.LeastRole(declared))
		}
		if hasRole && scope.ClaimOf(declared) == "" {
			t.Errorf("%v is user-grantable and names no claim a token could carry", declared)
		}
		if !hasRole && scope.ClaimOf(declared) != "" {
			t.Errorf("%v has no least role and names the claim %q, so a token could carry what no person holds", declared, scope.ClaimOf(declared))
		}
	}
	for _, declared := range []scopev1.Scope{scopev1.Scope_SCOPE_JOB, scopev1.Scope_SCOPE_TARGET_CREDENTIAL, scopev1.Scope_SCOPE_UNSPECIFIED} {
		if scope.IsUserScope(declared) {
			t.Errorf("IsUserScope(%v) = true, want false: only the kind of token grants it", declared)
		}
	}
}

func TestAClaimNamesTheScopeThatDeclaresIt(t *testing.T) {
	t.Parallel()

	for _, declared := range scope.ListUserScopes() {
		got, ok := scope.ScopeOfClaim(scope.ClaimOf(declared))
		if !ok || got != declared {
			t.Errorf("ScopeOfClaim(%q) = (%v, %v), want %v", scope.ClaimOf(declared), got, ok, declared)
		}
	}
	if _, ok := scope.ScopeOfClaim(""); ok {
		t.Error("the empty claim names a scope")
	}
	if _, ok := scope.ScopeOfClaim("cloud.provider"); ok {
		t.Error("a claim no scope declares names one")
	}
}

func TestAClaimNamesTheRoleThatDeclaresIt(t *testing.T) {
	t.Parallel()

	for _, declared := range []scopev1.Role{scopev1.Role_ROLE_MEMBER, scopev1.Role_ROLE_ADMIN, scopev1.Role_ROLE_OWNER} {
		got, ok := scope.RoleOfClaim(scope.ClaimOf(declared))
		if !ok || got != declared {
			t.Errorf("RoleOfClaim(%q) = (%v, %v), want %v", scope.ClaimOf(declared), got, ok, declared)
		}
	}
	for _, claim := range []string{"", "apps.write", "superuser"} {
		if got, ok := scope.RoleOfClaim(claim); ok {
			t.Errorf("RoleOfClaim(%q) = %v, want no role", claim, got)
		}
	}
}

func TestADestroyJobNeedsAnAdminAndEveryOtherCommandAMember(t *testing.T) {
	t.Parallel()

	values := runnerv1.JobCommand(0).Descriptor().Values()
	for i := range values.Len() {
		command := runnerv1.JobCommand(values.Get(i).Number())
		want := scopev1.Role_ROLE_MEMBER
		switch command {
		case runnerv1.JobCommand_JOB_COMMAND_UNSPECIFIED:
			want = scopev1.Role_ROLE_UNSPECIFIED
		case runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY:
			want = scopev1.Role_ROLE_ADMIN
		}
		if got := scope.LeastRole(command); got != want {
			t.Errorf("LeastRole(%v) = %v, want %v", command, got, want)
		}
	}
}
