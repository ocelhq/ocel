package consolev1_test

import (
	"strings"
	"testing"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func validIssueCloudTokenRequest() *consolev1.IssueCloudTokenRequest {
	return &consolev1.IssueCloudTokenRequest{OrganizationId: "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79", Scopes: []string{"cloud.apps.read"}}
}

func TestProtovalidateAcceptsEveryCloudTokenRequestTheCLICanSend(t *testing.T) {
	requireAccepted(t, "a describe", &consolev1.DescribeRequest{})
	requireAccepted(t, "one scope", validIssueCloudTokenRequest())
	scopes := validIssueCloudTokenRequest()
	scopes.Scopes = []string{"cloud.apps.read", "cloud.apps.write", "cloud.variables.write"}
	requireAccepted(t, "several scopes", scopes)
}

func TestProtovalidateRefusesACloudTokenRequestWithAMalformedScopeOrNoOrganization(t *testing.T) {
	requireRefusals(t, validIssueCloudTokenRequest, []refusal[*consolev1.IssueCloudTokenRequest]{
		{name: "an organization is named", mutate: func(r *consolev1.IssueCloudTokenRequest) { r.OrganizationId = "" }},
		{name: "an organization id is URL-safe", mutate: func(r *consolev1.IssueCloudTokenRequest) { r.OrganizationId = "../org_2" }},
		{name: "a scope is named", mutate: func(r *consolev1.IssueCloudTokenRequest) { r.Scopes = nil }},
		{name: "a scope is not empty", mutate: func(r *consolev1.IssueCloudTokenRequest) { r.Scopes = []string{""} }},
		{name: "a scope is lower case", mutate: func(r *consolev1.IssueCloudTokenRequest) { r.Scopes = []string{"Jobs"} }},
		{name: "a scope has no spaces", mutate: func(r *consolev1.IssueCloudTokenRequest) { r.Scopes = []string{"jobs write"} }},
		{name: "a scope is at most 64 characters", mutate: func(r *consolev1.IssueCloudTokenRequest) { r.Scopes = []string{strings.Repeat("s", 65)} }},
		{name: "each scope is named once", mutate: func(r *consolev1.IssueCloudTokenRequest) { r.Scopes = []string{"jobs", "jobs"} }},
		{name: "at most 32 scopes", mutate: func(r *consolev1.IssueCloudTokenRequest) {
			r.Scopes = nil
			for i := range 33 {
				r.Scopes = append(r.Scopes, "scope"+strings.Repeat("s", i))
			}
		}},
	})
}
