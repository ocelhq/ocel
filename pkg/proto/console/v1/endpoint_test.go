package consolev1_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const endpointID = "0199b5c4-0000-7000-8000-00000000cafe"

func validUpsertEndpointRequest() *consolev1.UpsertEndpointRequest {
	return &consolev1.UpsertEndpointRequest{
		Name:   "build-box",
		Target: "vps/sha256:abc/ocel",
		Vendor: "vps",
		Reach:  consolev1.EndpointReach_ENDPOINT_REACH_DIAL,
	}
}

func validSetEndpointAddressRequest() *consolev1.SetEndpointAddressRequest {
	return &consolev1.SetEndpointAddressRequest{
		Id:  endpointID,
		Url: "https://connector.example.test",
	}
}

func validHeartbeatEndpointRequest() *consolev1.HeartbeatEndpointRequest {
	return &consolev1.HeartbeatEndpointRequest{
		Id:            endpointID,
		Version:       "0.0.0-alpha",
		Services:      []string{"provider.variablestore.v1.VariableStoreService", "runner.v1.RunnerService"},
		AllowedScopes: []scopev1.Scope{scopev1.Scope_SCOPE_VARIABLES_READ},
	}
}

func TestProtovalidateAcceptsEveryEndpointTheCLICanRegister(t *testing.T) {
	requireAccepted(t, "a name, a target, a vendor and the way to reach it", validUpsertEndpointRequest())
	outside := validUpsertEndpointRequest()
	outside.Target = ""
	requireAccepted(t, "an endpoint in no target", outside)

	paired := validSetEndpointAddressRequest()
	paired.PublicKey = proto.String("O2onvM62pC1io6jQKm8Nc2UyFXcd4kOmOsBIoYtZ2ik=")
	paired.TlsPin = proto.String("sha256:abc")
	paired.Compute = consolev1.ComputeKind_COMPUTE_KIND_CONTAINER
	requireAccepted(t, "an address with a key, a pin and a compute", paired)
	requireAccepted(t, "an address with only a url", validSetEndpointAddressRequest())

	requireAccepted(t, "a removal by id", &consolev1.RemoveEndpointRequest{Id: endpointID})
	requireAccepted(t, "a list", &consolev1.ListEndpointsRequest{})
	requireAccepted(t, "a heartbeat with a version and grants", validHeartbeatEndpointRequest())
	requireAccepted(t, "a heartbeat with no grants", &consolev1.HeartbeatEndpointRequest{Id: endpointID})
}

func TestProtovalidateRefusesAEndpointTheConsoleCannotStore(t *testing.T) {
	requireRefusals(t, validUpsertEndpointRequest, []refusal[*consolev1.UpsertEndpointRequest]{
		{name: "the name is required", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Name = "" }},
		{name: "the name is a DNS label", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Name = "Build_Box" }},
		{name: "the name is not Ocel Cloud's", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Name = "ocel-cloud" }, rule: "upsert_endpoint.name_not_ocel_cloud"},
		{name: "the target is not Ocel Cloud's", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Target = "ocel/cloud" }, rule: "upsert_endpoint.target_not_ocel_cloud"},
		{name: "the target names a vendor and an account", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Target = "vps" }},
		{name: "the target is lower case", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Target = "VPS/abc" }},
		{name: "the target is at most 256 characters", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Target = "vps/" + strings.Repeat("a", 253) }},
		{name: "the vendor is required", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Vendor = "" }},
		{name: "the vendor is lower case", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Vendor = "VPS" }},
		{name: "the vendor is at most 64 characters", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Vendor = strings.Repeat("v", 65) }},
		{name: "the reach is required", mutate: func(r *consolev1.UpsertEndpointRequest) {
			r.Reach = consolev1.EndpointReach_ENDPOINT_REACH_UNSPECIFIED
		}},
		{name: "the reach is one the console knows", mutate: func(r *consolev1.UpsertEndpointRequest) { r.Reach = 99 }},
	})

	requireRefusals(t, validSetEndpointAddressRequest, []refusal[*consolev1.SetEndpointAddressRequest]{
		{name: "the id is required", mutate: func(r *consolev1.SetEndpointAddressRequest) { r.Id = "" }},
		{name: "the id is a uuid", mutate: func(r *consolev1.SetEndpointAddressRequest) { r.Id = "con_1" }},
		{name: "the url is required", mutate: func(r *consolev1.SetEndpointAddressRequest) { r.Url = "" }},
		{name: "the url is a url", mutate: func(r *consolev1.SetEndpointAddressRequest) { r.Url = "connector" }},
		{name: "the url is at most 2048 characters", mutate: func(r *consolev1.SetEndpointAddressRequest) {
			r.Url = "https://example.test/" + strings.Repeat("a", 2048)
		}},
		{name: "the public key is not empty", mutate: func(r *consolev1.SetEndpointAddressRequest) { r.PublicKey = proto.String("") }},
		{name: "the tls pin is not empty", mutate: func(r *consolev1.SetEndpointAddressRequest) { r.TlsPin = proto.String("") }},
		{name: "the compute is one the console knows", mutate: func(r *consolev1.SetEndpointAddressRequest) { r.Compute = 99 }},
	})

	requireRefusals(t, func() *consolev1.RemoveEndpointRequest {
		return &consolev1.RemoveEndpointRequest{Id: endpointID}
	}, []refusal[*consolev1.RemoveEndpointRequest]{
		{name: "the id is required", mutate: func(r *consolev1.RemoveEndpointRequest) { r.Id = "" }},
		{name: "the id is a uuid", mutate: func(r *consolev1.RemoveEndpointRequest) { r.Id = "con_1" }},
	})

	requireRefusals(t, validHeartbeatEndpointRequest, []refusal[*consolev1.HeartbeatEndpointRequest]{
		{name: "the id is required", mutate: func(r *consolev1.HeartbeatEndpointRequest) { r.Id = "" }},
		{name: "the id is a uuid", mutate: func(r *consolev1.HeartbeatEndpointRequest) { r.Id = "con_1" }},
		{name: "the version is at most 64 characters", mutate: func(r *consolev1.HeartbeatEndpointRequest) { r.Version = strings.Repeat("v", 65) }},
		{name: "a service is a proto service name", mutate: func(r *consolev1.HeartbeatEndpointRequest) { r.Services = []string{"variables"} }},
		{name: "each service is named once", mutate: func(r *consolev1.HeartbeatEndpointRequest) {
			r.Services = []string{"runner.v1.RunnerService", "runner.v1.RunnerService"}
		}},
		{name: "an allowed scope is named", mutate: func(r *consolev1.HeartbeatEndpointRequest) {
			r.AllowedScopes = []scopev1.Scope{scopev1.Scope_SCOPE_UNSPECIFIED}
		}},
		{name: "an allowed scope is one the contract defines", mutate: func(r *consolev1.HeartbeatEndpointRequest) { r.AllowedScopes = []scopev1.Scope{99} }},
	})
}

func validBindRunnerRequest() *consolev1.BindRunnerRequest {
	return &consolev1.BindRunnerRequest{ProjectId: projectID, Tier: environmentv1.Tier_TIER_PRODUCTION, RunnerId: endpointID, Credentials: true}
}

func TestProtovalidateAcceptsEveryRunnerBindingAndResolution(t *testing.T) {
	requireAccepted(t, "a registered runner with credentials", validBindRunnerRequest())
	requireAccepted(t, "Ocel Cloud's runner", &consolev1.BindRunnerRequest{ProjectId: projectID, Tier: environmentv1.Tier_TIER_PREVIEW, RunnerId: "ocel-cloud"})
	requireAccepted(t, "an unbinding", &consolev1.UnbindRunnerRequest{ProjectId: projectID, Tier: environmentv1.Tier_TIER_PREVIEW})
	requireAccepted(t, "a resolution for a project naming a provider", &consolev1.ResolveRunnerRequest{ProjectId: projectID, Tier: environmentv1.Tier_TIER_PRODUCTION, Provider: "aws"})
	requireAccepted(t, "a resolution for a project naming none", &consolev1.ResolveRunnerRequest{ProjectId: projectID, Tier: environmentv1.Tier_TIER_PRODUCTION})
}

func TestProtovalidateRefusesARunnerBindingTheConsoleCannotStore(t *testing.T) {
	requireRefusals(t, validBindRunnerRequest, []refusal[*consolev1.BindRunnerRequest]{
		{name: "the project is a uuid", mutate: func(r *consolev1.BindRunnerRequest) { r.ProjectId = "shop" }},
		{name: "the tier is named", mutate: func(r *consolev1.BindRunnerRequest) { r.Tier = environmentv1.Tier_TIER_UNSPECIFIED }},
		{name: "the runner is a registered endpoint or Ocel Cloud's", mutate: func(r *consolev1.BindRunnerRequest) { r.RunnerId = "my-box" }},
	})
}
