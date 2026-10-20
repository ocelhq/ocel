package consolev1_test

import (
	"strings"
	"testing"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func validIssueCloudTokenRequest() *consolev1.IssueCloudTokenRequest {
	return &consolev1.IssueCloudTokenRequest{Scopes: []string{"jobs:write"}}
}

func TestProtovalidateAcceptsEveryCloudTokenRequestTheCLICanSend(t *testing.T) {
	requireAccepted(t, "a describe", &consolev1.DescribeRequest{})
	requireAccepted(t, "the default scopes", &consolev1.IssueCloudTokenRequest{})
	requireAccepted(t, "named scopes", &consolev1.IssueCloudTokenRequest{Scopes: []string{"jobs:write", "source.upload"}})
}

func TestProtovalidateRefusesACloudTokenRequestWithAMalformedScope(t *testing.T) {
	requireRefusals(t, validIssueCloudTokenRequest, []refusal[*consolev1.IssueCloudTokenRequest]{
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
