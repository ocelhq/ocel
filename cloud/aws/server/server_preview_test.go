package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestPreviewExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name      string
		lifecycle deploymentsv1.Environment_Lifecycle
		want      int64
	}{
		{"ephemeral gets now+ttl", deploymentsv1.Environment_LIFECYCLE_EPHEMERAL, now.Add(previewTTL).Unix()},
		{"persistent has no expiry", deploymentsv1.Environment_LIFECYCLE_PERSISTENT, 0},
		{"unspecified (production) has no expiry", deploymentsv1.Environment_LIFECYCLE_UNSPECIFIED, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := previewExpiry(tc.lifecycle, now); got != tc.want {
				t.Errorf("previewExpiry(%v) = %d, want %d", tc.lifecycle, got, tc.want)
			}
		})
	}
}

func TestStackName(t *testing.T) {
	cases := []struct {
		name string
		env  *deploymentsv1.Environment
		want string
	}{
		{"nil env keeps production", nil, "proj-123-prod"},
		{
			"production class keeps production",
			&deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PRODUCTION},
			"proj-123-prod",
		},
		{
			"preview ephemeral isolates by identity",
			&deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL, Identity: "feature_login_ab12"},
			"proj-123-preview-feature_login_ab12",
		},
		{
			"preview persistent isolates by identity",
			&deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT, Identity: "staging"},
			"proj-123-preview-staging",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stackName("proj-123", tc.env); got != tc.want {
				t.Errorf("stackName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreflightResponse(t *testing.T) {
	preview := bootstrap.Deployed{Present: true, Class: bootstrap.ClassPreview}
	production := bootstrap.Deployed{Present: true, Class: bootstrap.ClassProduction}
	absent := bootstrap.Deployed{Present: false}

	cases := []struct {
		name          string
		required      deploymentsv1.Environment_Class
		preview, prod bootstrap.Deployed
		wantClass     deploymentsv1.Environment_Class
		wantPresent   bool
	}{
		// Both substrates present: each command gates against its own class,
		// never a spurious mismatch (the bug this scoping fixes).
		{"deploy with both reports production", deploymentsv1.Environment_CLASS_PRODUCTION, preview, production, deploymentsv1.Environment_CLASS_PRODUCTION, true},
		{"preview with both reports preview", deploymentsv1.Environment_CLASS_PREVIEW, preview, production, deploymentsv1.Environment_CLASS_PREVIEW, true},
		// Required substrate present alone.
		{"preview required, preview present", deploymentsv1.Environment_CLASS_PREVIEW, preview, absent, deploymentsv1.Environment_CLASS_PREVIEW, true},
		{"production required, production present", deploymentsv1.Environment_CLASS_PRODUCTION, absent, production, deploymentsv1.Environment_CLASS_PRODUCTION, true},
		// Wrong account: required absent, the other present -> report the other
		// so the caller's guard fires an informative mismatch.
		{"deploy in a preview-only account reports preview", deploymentsv1.Environment_CLASS_PRODUCTION, preview, absent, deploymentsv1.Environment_CLASS_PREVIEW, true},
		{"preview in a production-only account reports production", deploymentsv1.Environment_CLASS_PREVIEW, absent, production, deploymentsv1.Environment_CLASS_PRODUCTION, true},
		// Empty account.
		{"empty account reports absent", deploymentsv1.Environment_CLASS_PREVIEW, absent, absent, deploymentsv1.Environment_CLASS_UNSPECIFIED, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preflightResponse(tc.required, tc.preview, tc.prod)
			if got.GetInfraClass() != tc.wantClass || got.GetInfrastructurePresent() != tc.wantPresent {
				t.Errorf("preflightResponse() = {class=%v present=%v}, want {class=%v present=%v}",
					got.GetInfraClass(), got.GetInfrastructurePresent(), tc.wantClass, tc.wantPresent)
			}
		})
	}
}

// routeOwners is a stand-in edge lookup: who holds each pattern, and a failure
// for the patterns it is told to fail on.
func routeOwners(byPattern map[string]string, fail map[string]bool) routeOwnerFunc {
	return func(_ context.Context, pattern string) (string, error) {
		if fail[pattern] {
			return "", errors.New("cloudflare said no")
		}
		return byPattern[pattern], nil
	}
}

func TestDomainClaims_AnswersInRequestOrderWithTheOwningScript(t *testing.T) {
	owner := routeOwners(map[string]string{
		"*.preview.app.com/*": "ocel-other--preview",
		"shop.com/*":          "ocel-shop--prod-web",
	}, nil)

	got := domainClaims(context.Background(), owner, "shop", []string{"*.preview.app.com", "free.com", "shop.com"})

	if len(got) != 3 {
		t.Fatalf("claims = %d, want one per requested hostname", len(got))
	}
	if got[0].GetHostname() != "*.preview.app.com" || got[0].GetStatus() != deploymentsv1.DomainClaim_STATUS_CLAIMED || got[0].GetOwner() != "ocel-other--preview" {
		t.Errorf("claim[0] = %+v, want another project's worker reported as the owner", got[0])
	}
	if got[1].GetHostname() != "free.com" || got[1].GetStatus() != deploymentsv1.DomainClaim_STATUS_UNCLAIMED || got[1].GetOwner() != "" {
		t.Errorf("claim[1] = %+v, want an unheld hostname reported unclaimed", got[1])
	}
	// This project's own worker is not a conflict: redeploying reclaims the
	// hostname idempotently, and refusing there would refuse every redeploy.
	if got[2].GetStatus() != deploymentsv1.DomainClaim_STATUS_UNCLAIMED || got[2].GetOwner() != "" {
		t.Errorf("claim[2] = %+v, want this project's own hold to read as free to take", got[2])
	}
}

// "Nobody holds it" and "nobody could say" must never collapse: a lookup the
// edge could not answer leaves the status unspecified, so the CLI skips the
// guard instead of failing a deploy it cannot verify.
func TestDomainClaims_AnEdgeThatCannotAnswerIsUnspecified(t *testing.T) {
	failing := domainClaims(context.Background(), routeOwners(nil, map[string]bool{"app.com/*": true}), "shop", []string{"app.com"})
	if len(failing) != 1 || failing[0].GetHostname() != "app.com" || failing[0].GetStatus() != deploymentsv1.DomainClaim_STATUS_UNSPECIFIED {
		t.Errorf("claims = %+v, want a failed lookup reported as unanswerable", failing)
	}
}

// The check is opt-in per the request: no hostnames means no lookup is paid for.
func TestDomainClaims_NoDomainsAsksTheEdgeNothing(t *testing.T) {
	asked := 0
	owner := func(context.Context, string) (string, error) {
		asked++
		return "", nil
	}
	if got := domainClaims(context.Background(), owner, "shop", nil); got != nil {
		t.Errorf("claims = %+v, want none", got)
	}
	if asked != 0 {
		t.Errorf("edge lookups = %d, want 0", asked)
	}
}

// knownSlugs must answer without opening a Pulumi backend whenever the drift
// check can't or needn't run — a caller that sent no slug, or a substrate that
// isn't bootstrapped. A zero aws.Config makes any AWS call the guard fails to
// skip fail loudly rather than pass by luck.
func TestKnownSlugs_SkipsTheBackendWhenTheCheckCannotRun(t *testing.T) {
	present := bootstrap.Deployed{Present: true, StateBucket: "ocel-state"}

	cases := []struct {
		name      string
		substrate bootstrap.Deployed
		slug      string
	}{
		{"no slug sent", present, ""},
		{"substrate not bootstrapped", bootstrap.Deployed{Present: false}, "my-app"},
		{"substrate present but has no state bucket", bootstrap.Deployed{Present: true}, "my-app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := knownSlugs(context.Background(), aws.Config{}, tc.substrate, tc.slug); got != nil {
				t.Errorf("knownSlugs() = %v, want nil", got)
			}
		})
	}
}

func TestToPreviewEnvironments(t *testing.T) {
	stacks := []deploy.PreviewStack{
		{Identity: "feature_login_ab12", Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL, Label: "pr-7", CreatedAt: 100, ExpiresAt: 200},
		{Identity: "staging", Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT},
	}

	got := toPreviewEnvironments(stacks)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].GetIdentity() != "feature_login_ab12" || got[0].GetLifecycle() != deploymentsv1.Environment_LIFECYCLE_EPHEMERAL ||
		got[0].GetLabel() != "pr-7" || got[0].GetCreatedAt() != 100 || got[0].GetExpiresAt() != 200 {
		t.Errorf("first env = %+v, want the ephemeral entry mapped through", got[0])
	}
	if got[1].GetIdentity() != "staging" || got[1].GetLifecycle() != deploymentsv1.Environment_LIFECYCLE_PERSISTENT {
		t.Errorf("second env = %+v, want the persistent entry mapped through", got[1])
	}
}

func TestToPreviewEnvironments_Empty(t *testing.T) {
	if got := toPreviewEnvironments(nil); len(got) != 0 {
		t.Errorf("toPreviewEnvironments(nil) = %+v, want empty", got)
	}
}
