package scopev1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func TestAScopeAPersonCanHoldNamesItsClaimAndLeastRoleAndOneNoPersonHoldsNamesNeither(t *testing.T) {
	values := scopev1.Scope(0).Descriptor().Values()
	claims := map[string]bool{}
	for i := range values.Len() {
		value := values.Get(i)
		claim := proto.GetExtension(value.Options(), scopev1.E_Claim).(string)
		role := proto.GetExtension(value.Options(), scopev1.E_LeastRole).(scopev1.Role)
		switch scopev1.Scope(value.Number()) {
		case scopev1.Scope_SCOPE_UNSPECIFIED, scopev1.Scope_SCOPE_JOB, scopev1.Scope_SCOPE_TARGET_CREDENTIAL:
			if claim != "" || role != scopev1.Role_ROLE_UNSPECIFIED {
				t.Errorf("%s names the claim %q and the role %s, want neither: only the kind of token grants it", value.Name(), claim, role)
			}
		default:
			if claim == "" || claims[claim] || role == scopev1.Role_ROLE_UNSPECIFIED {
				t.Errorf("%s names the claim %q and the role %s, want a claim no other scope names and a least role", value.Name(), claim, role)
			}
			claims[claim] = true
		}
	}
}

func TestEveryMethodAnEndpointServesDeclaresTheScopeItNeeds(t *testing.T) {
	services := []protoreflect.ServiceDescriptor{
		contractv1.File_provider_contract_v1_contract_proto.Services().ByName("ProviderService"),
		variablestorev1.File_provider_variablestore_v1_variablestore_proto.Services().ByName("VariableStoreService"),
		costv1.File_provider_cost_v1_cost_proto.Services().ByName("CostService"),
		runnerv1.File_runner_v1_runner_proto.Services().ByName("RunnerService"),
		runnerv1.File_runner_v1_jobagent_proto.Services().ByName("JobAgentService"),
		endpointv1.File_endpoint_v1_endpoint_proto.Services().ByName("SshCertificateService"),
	}
	for _, service := range services {
		methods := service.Methods()
		for i := range methods.Len() {
			method := methods.Get(i)
			if required := proto.GetExtension(method.Options(), scopev1.E_Required).(scopev1.Scope); required == scopev1.Scope_SCOPE_UNSPECIFIED {
				t.Errorf("%s declares no scope, so every endpoint refuses it", method.FullName())
			}
		}
	}
}

func TestEveryJobCommandNamesTheLeastRoleThatMayDispatchIt(t *testing.T) {
	values := runnerv1.JobCommand(0).Descriptor().Values()
	for i := 1; i < values.Len(); i++ {
		value := values.Get(i)
		if role := proto.GetExtension(value.Options(), scopev1.E_LeastRole).(scopev1.Role); role == scopev1.Role_ROLE_UNSPECIFIED {
			t.Errorf("%s names no least role, so the console cannot say who may dispatch it", value.Name())
		}
	}
}
