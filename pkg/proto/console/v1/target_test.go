package consolev1_test

import (
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const (
	targetID        = "0199d1e2-0000-7000-8000-00000000beef"
	targetProjectID = "0199d1e2-0000-7000-8000-00000000f00d"
)

func validPrepareTargetRequest() *consolev1.PrepareTargetRequest {
	return &consolev1.PrepareTargetRequest{Vendor: "aws", Account: "123456789012", Region: "eu-west-1"}
}

func validTierIdentity() *consolev1.TierIdentity {
	return &consolev1.TierIdentity{
		IdentityProvider: "arn:aws:iam::123456789012:oidc-provider/console.example.test/oidc",
		Principal:        "arn:aws:iam::123456789012:role/ocel-deploy-production",
	}
}

func validRecordTargetTierRequest() *consolev1.RecordTargetTierRequest {
	return &consolev1.RecordTargetTierRequest{
		TargetId:   targetID,
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Identity:   validTierIdentity(),
		CliVersion: "0.0.0-alpha",
	}
}

func validIssueCredentialRequest() *consolev1.IssueCredentialRequest {
	return &consolev1.IssueCredentialRequest{
		ProjectId:  targetProjectID,
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Vendor:     "gcp",
		CliVersion: "0.0.0-alpha",
	}
}

func validForgetTargetTierRequest() *consolev1.ForgetTargetTierRequest {
	return &consolev1.ForgetTargetTierRequest{TargetId: targetID, Tier: environmentv1.Tier_TIER_PREVIEW}
}

func TestProtovalidateAcceptsEveryTargetRequestBootstrapAndAPipelineCanSend(t *testing.T) {
	requireAccepted(t, "a vendor, an account and a region", validPrepareTargetRequest())

	linked := validPrepareTargetRequest()
	linked.ProjectId = targetProjectID
	requireAccepted(t, "a prepare from a linked checkout", linked)

	gcp := &consolev1.PrepareTargetRequest{Vendor: "gcp", Account: "shop-prod-4821", Region: "europe-west1"}
	requireAccepted(t, "a project id as the account", gcp)

	requireAccepted(t, "a tier's identity", validRecordTargetTierRequest())

	bound := validRecordTargetTierRequest()
	bound.ProjectId = targetProjectID
	requireAccepted(t, "a tier bound to a project", bound)

	federated := validRecordTargetTierRequest()
	federated.Identity = &consolev1.TierIdentity{
		IdentityProvider: "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/ocel/providers/console",
		Principal:        "ocel-deploy-preview@shop-prod-4821.iam.gserviceaccount.com",
	}
	requireAccepted(t, "a service account behind a workload identity provider", federated)

	requireAccepted(t, "a credential for one project tier", validIssueCredentialRequest())

	requireAccepted(t, "a tier to forget", validForgetTargetTierRequest())
}

func TestProtovalidateRefusesATargetRequestTheConsoleCannotTrust(t *testing.T) {
	requireRefusals(t, validPrepareTargetRequest, []refusal[*consolev1.PrepareTargetRequest]{
		{name: "the vendor is required", mutate: func(r *consolev1.PrepareTargetRequest) { r.Vendor = "" }},
		{name: "the vendor is lower case", mutate: func(r *consolev1.PrepareTargetRequest) { r.Vendor = "AWS" }},
		{name: "the account is required", mutate: func(r *consolev1.PrepareTargetRequest) { r.Account = "" }},
		{name: "the account has no slash", mutate: func(r *consolev1.PrepareTargetRequest) { r.Account = "123/456" }},
		{name: "the account is at most 128 characters", mutate: func(r *consolev1.PrepareTargetRequest) { r.Account = strings.Repeat("a", 129) }},
		{name: "the region is required", mutate: func(r *consolev1.PrepareTargetRequest) { r.Region = "" }},
		{name: "the region is lower case", mutate: func(r *consolev1.PrepareTargetRequest) { r.Region = "EU-WEST-1" }},
		{name: "the region is at most 64 characters", mutate: func(r *consolev1.PrepareTargetRequest) { r.Region = strings.Repeat("r", 65) }},
		{name: "the project id is a uuid", mutate: func(r *consolev1.PrepareTargetRequest) { r.ProjectId = "shop" }},
	})

	requireRefusals(t, validRecordTargetTierRequest, []refusal[*consolev1.RecordTargetTierRequest]{
		{name: "the target id is required", mutate: func(r *consolev1.RecordTargetTierRequest) { r.TargetId = "" }},
		{name: "the target id is a uuid", mutate: func(r *consolev1.RecordTargetTierRequest) { r.TargetId = "tgt_1" }},
		{name: "the tier is required", mutate: func(r *consolev1.RecordTargetTierRequest) { r.Tier = environmentv1.Tier_TIER_UNSPECIFIED }},
		{name: "the tier is one the console knows", mutate: func(r *consolev1.RecordTargetTierRequest) { r.Tier = 99 }},
		{name: "the identity is required", mutate: func(r *consolev1.RecordTargetTierRequest) { r.Identity = nil }},
		{name: "the identity provider is required", mutate: func(r *consolev1.RecordTargetTierRequest) { r.Identity.IdentityProvider = "" }},
		{name: "the identity provider has no spaces", mutate: func(r *consolev1.RecordTargetTierRequest) { r.Identity.IdentityProvider = "arn:aws:iam::1 :oidc" }},
		{name: "the principal is required", mutate: func(r *consolev1.RecordTargetTierRequest) { r.Identity.Principal = "" }},
		{name: "the principal is at most 2048 characters", mutate: func(r *consolev1.RecordTargetTierRequest) {
			r.Identity.Principal = "arn:aws:iam::123456789012:role/" + strings.Repeat("r", 2048)
		}},
		{name: "the cli version is required", mutate: func(r *consolev1.RecordTargetTierRequest) { r.CliVersion = "" }},
		{name: "the cli version is at most 64 characters", mutate: func(r *consolev1.RecordTargetTierRequest) { r.CliVersion = strings.Repeat("v", 65) }},
		{name: "the project id is a uuid", mutate: func(r *consolev1.RecordTargetTierRequest) { r.ProjectId = "shop" }},
	})

	requireRefusals(t, validIssueCredentialRequest, []refusal[*consolev1.IssueCredentialRequest]{
		{name: "the project id is required", mutate: func(r *consolev1.IssueCredentialRequest) { r.ProjectId = "" }},
		{name: "the project id is a uuid", mutate: func(r *consolev1.IssueCredentialRequest) { r.ProjectId = "shop" }},
		{name: "the tier is required", mutate: func(r *consolev1.IssueCredentialRequest) { r.Tier = environmentv1.Tier_TIER_UNSPECIFIED }},
		{name: "the vendor is required", mutate: func(r *consolev1.IssueCredentialRequest) { r.Vendor = "" }},
		{name: "the vendor is lower case", mutate: func(r *consolev1.IssueCredentialRequest) { r.Vendor = "GCP" }},
		{name: "the cli version is required", mutate: func(r *consolev1.IssueCredentialRequest) { r.CliVersion = "" }},
	})

	requireRefusals(t, validForgetTargetTierRequest, []refusal[*consolev1.ForgetTargetTierRequest]{
		{name: "the target id is a uuid", mutate: func(r *consolev1.ForgetTargetTierRequest) { r.TargetId = "tgt_1" }},
		{name: "the tier is required", mutate: func(r *consolev1.ForgetTargetTierRequest) { r.Tier = environmentv1.Tier_TIER_UNSPECIFIED }},
	})
}
