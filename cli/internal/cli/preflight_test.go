package cli

import (
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestFormatIdentityBanner_AWSAndCloudflare(t *testing.T) {
	got := formatIdentityBanner(&deploymentsv1.Identity{
		AwsProfile:        "default",
		AwsAccount:        "123456789012",
		AwsRegion:         "us-east-1",
		AwsArn:            "arn:aws:iam::123456789012:user/deploy",
		CloudflareAccount: "abcd1234",
	})
	for _, want := range []string{"Running with:", "profile=default", "account=123456789012", "region=us-east-1", "Cloudflare", "abcd1234"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing %q:\n%s", want, got)
		}
	}
}

func TestFormatIdentityBanner_ProfileFallsBackToArnPrincipal(t *testing.T) {
	got := formatIdentityBanner(&deploymentsv1.Identity{
		AwsAccount: "123456789012",
		AwsRegion:  "eu-west-1",
		AwsArn:     "arn:aws:sts::123456789012:assumed-role/Deployer/session",
	})
	if strings.Contains(got, "profile=") {
		t.Errorf("expected no profile= when AWS_PROFILE unset:\n%s", got)
	}
	if !strings.Contains(got, "identity=session") {
		t.Errorf("expected identity fallback to arn principal:\n%s", got)
	}
}

func TestFormatIdentityBanner_EmptyIsBlank(t *testing.T) {
	if got := formatIdentityBanner(&deploymentsv1.Identity{}); got != "" {
		t.Errorf("expected blank banner for empty identity, got %q", got)
	}
	if got := formatIdentityBanner(nil); got != "" {
		t.Errorf("expected blank banner for nil identity, got %q", got)
	}
}

func TestCredentialProblems_NilWhenNone(t *testing.T) {
	if err := credentialProblems(nil); err != nil {
		t.Errorf("expected nil error for no problems, got %v", err)
	}
}

func TestCredentialProblems_AggregatesAll(t *testing.T) {
	err := credentialProblems([]*deploymentsv1.CredentialProblem{
		{Provider: "AWS", Message: "could not authenticate", Hint: "run aws sso login"},
		{Provider: "Cloudflare", Message: "CLOUDFLARE_API_TOKEN is not set", Hint: "export it"},
	})
	if err == nil {
		t.Fatal("expected an error aggregating the problems")
	}
	for _, want := range []string{"AWS", "could not authenticate", "run aws sso login", "Cloudflare", "CLOUDFLARE_API_TOKEN is not set", "export it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error missing %q:\n%s", want, err.Error())
		}
	}
}
