package consolev1_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const connectorID = "0199b5c4-0000-7000-8000-00000000cafe"

func validUpsertConnectorRequest() *consolev1.UpsertConnectorRequest {
	return &consolev1.UpsertConnectorRequest{
		Target: "vps/sha256:abc/ocel",
		Vendor: "vps",
		Reach:  consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
	}
}

func validSetConnectorAddressRequest() *consolev1.SetConnectorAddressRequest {
	return &consolev1.SetConnectorAddressRequest{
		Id:  connectorID,
		Url: "https://connector.example.test",
	}
}

func TestProtovalidateAcceptsEveryConnectorTheCLICanRegister(t *testing.T) {
	requireAccepted(t, "a target, a vendor and the way to reach it", validUpsertConnectorRequest())

	paired := validSetConnectorAddressRequest()
	paired.PublicKey = proto.String("O2onvM62pC1io6jQKm8Nc2UyFXcd4kOmOsBIoYtZ2ik=")
	paired.TlsPin = proto.String("sha256:abc")
	paired.Compute = consolev1.ComputeKind_COMPUTE_KIND_CONTAINER
	requireAccepted(t, "an address with a key, a pin and a compute", paired)
	requireAccepted(t, "an address with only a url", validSetConnectorAddressRequest())

	requireAccepted(t, "a removal by id", &consolev1.RemoveConnectorRequest{Id: connectorID})
	requireAccepted(t, "a list", &consolev1.ListConnectorsRequest{})
}

func TestProtovalidateRefusesAConnectorTheConsoleCannotStore(t *testing.T) {
	requireRefusals(t, validUpsertConnectorRequest, []refusal[*consolev1.UpsertConnectorRequest]{
		{name: "the target is required", mutate: func(r *consolev1.UpsertConnectorRequest) { r.Target = "" }},
		{name: "the target names a vendor and an account", mutate: func(r *consolev1.UpsertConnectorRequest) { r.Target = "vps" }},
		{name: "the target is lower case", mutate: func(r *consolev1.UpsertConnectorRequest) { r.Target = "VPS/abc" }},
		{name: "the target is at most 256 characters", mutate: func(r *consolev1.UpsertConnectorRequest) { r.Target = "vps/" + strings.Repeat("a", 253) }},
		{name: "the vendor is required", mutate: func(r *consolev1.UpsertConnectorRequest) { r.Vendor = "" }},
		{name: "the vendor is lower case", mutate: func(r *consolev1.UpsertConnectorRequest) { r.Vendor = "VPS" }},
		{name: "the vendor is at most 64 characters", mutate: func(r *consolev1.UpsertConnectorRequest) { r.Vendor = strings.Repeat("v", 65) }},
		{name: "the reach is required", mutate: func(r *consolev1.UpsertConnectorRequest) {
			r.Reach = consolev1.ConnectorReach_CONNECTOR_REACH_UNSPECIFIED
		}},
		{name: "the reach is one the console knows", mutate: func(r *consolev1.UpsertConnectorRequest) { r.Reach = 99 }},
	})

	requireRefusals(t, validSetConnectorAddressRequest, []refusal[*consolev1.SetConnectorAddressRequest]{
		{name: "the id is required", mutate: func(r *consolev1.SetConnectorAddressRequest) { r.Id = "" }},
		{name: "the id is a uuid", mutate: func(r *consolev1.SetConnectorAddressRequest) { r.Id = "con_1" }},
		{name: "the url is required", mutate: func(r *consolev1.SetConnectorAddressRequest) { r.Url = "" }},
		{name: "the url is a url", mutate: func(r *consolev1.SetConnectorAddressRequest) { r.Url = "connector" }},
		{name: "the url is at most 2048 characters", mutate: func(r *consolev1.SetConnectorAddressRequest) {
			r.Url = "https://example.test/" + strings.Repeat("a", 2048)
		}},
		{name: "the public key is not empty", mutate: func(r *consolev1.SetConnectorAddressRequest) { r.PublicKey = proto.String("") }},
		{name: "the tls pin is not empty", mutate: func(r *consolev1.SetConnectorAddressRequest) { r.TlsPin = proto.String("") }},
		{name: "the compute is one the console knows", mutate: func(r *consolev1.SetConnectorAddressRequest) { r.Compute = 99 }},
	})

	requireRefusals(t, func() *consolev1.RemoveConnectorRequest {
		return &consolev1.RemoveConnectorRequest{Id: connectorID}
	}, []refusal[*consolev1.RemoveConnectorRequest]{
		{name: "the id is required", mutate: func(r *consolev1.RemoveConnectorRequest) { r.Id = "" }},
		{name: "the id is a uuid", mutate: func(r *consolev1.RemoveConnectorRequest) { r.Id = "con_1" }},
	})
}
