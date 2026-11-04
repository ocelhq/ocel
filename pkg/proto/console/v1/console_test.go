package consolev1_test

import (
	"testing"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func validIssueEndpointTokenRequest() *consolev1.IssueEndpointTokenRequest {
	return &consolev1.IssueEndpointTokenRequest{OrganizationId: "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79", EndpointId: "ocel-cloud", Scopes: []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ}}
}

func TestProtovalidateAcceptsEveryEndpointTokenRequestTheCLICanSend(t *testing.T) {
	requireAccepted(t, "a describe", &consolev1.DescribeRequest{})
	requireAccepted(t, "one scope for Ocel Cloud", validIssueEndpointTokenRequest())
	registered := validIssueEndpointTokenRequest()
	registered.EndpointId = "0199b5c4-0000-7000-8000-00000000cafe"
	registered.Scopes = []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ, scopev1.Scope_SCOPE_APPS_WRITE, scopev1.Scope_SCOPE_VARIABLES_WRITE}
	requireAccepted(t, "several scopes for a registered endpoint", registered)
}

func TestProtovalidateRefusesAnEndpointTokenRequestWithAMalformedScopeOrNoEndpoint(t *testing.T) {
	requireRefusals(t, validIssueEndpointTokenRequest, []refusal[*consolev1.IssueEndpointTokenRequest]{
		{name: "an organization is named", mutate: func(r *consolev1.IssueEndpointTokenRequest) { r.OrganizationId = "" }},
		{name: "an organization id is URL-safe", mutate: func(r *consolev1.IssueEndpointTokenRequest) { r.OrganizationId = "../org_2" }},
		{name: "an endpoint is named", mutate: func(r *consolev1.IssueEndpointTokenRequest) { r.EndpointId = "" }},
		{name: "an endpoint is a uuid or Ocel Cloud's", mutate: func(r *consolev1.IssueEndpointTokenRequest) { r.EndpointId = "my-box" }},
		{name: "a scope is named", mutate: func(r *consolev1.IssueEndpointTokenRequest) { r.Scopes = nil }},
		{name: "a scope is not unspecified", mutate: func(r *consolev1.IssueEndpointTokenRequest) {
			r.Scopes = []scopev1.Scope{scopev1.Scope_SCOPE_UNSPECIFIED}
		}},
		{name: "a scope is one the contract defines", mutate: func(r *consolev1.IssueEndpointTokenRequest) { r.Scopes = []scopev1.Scope{99} }},
		{name: "each scope is named once", mutate: func(r *consolev1.IssueEndpointTokenRequest) {
			r.Scopes = []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ, scopev1.Scope_SCOPE_APPS_READ}
		}},
	})
}
