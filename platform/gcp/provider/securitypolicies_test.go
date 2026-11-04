package gcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func planningArmoredALB(t *testing.T, denied []string, skipped bool) error {
	t.Helper()
	server := &googletest.Run{Iam: googletest.GrantedIAM()}
	server.Iam.ProjectDeniedPermissions = denied
	p := servingTransformed(t, server, &patchingByKind{patches: map[string]transform.Patches{
		albresources.TransformKindEdge: {"backendService": {"securityPolicy": "web-armor"}},
	}})
	p.SkipChecks = skipped
	front, err := p.Edges().Open(albresources.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	spec.Edge = front
	_, err = p.Stacks().Plan(context.Background(), spec, &fake.Log{})
	return err
}

func TestAPlanSettingACloudArmorPolicyTheDeployCredentialMayNotUseIsRefusedNamingIt(t *testing.T) {
	err := planningArmoredALB(t, []string{securityPolicyUsePermission}, false)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, securityPolicyUsePermission) || !strings.Contains(refused.Message, "web-armor") {
		t.Errorf("Plan() = %v, want a denied refusal naming %s and the policy web-armor", err, securityPolicyUsePermission)
	}
}

func TestAPlanSettingACloudArmorPolicyIsNotCheckedWhenTheDeployCredentialMayUseItOrChecksAreSkipped(t *testing.T) {
	for name, tc := range map[string]struct {
		denied  []string
		skipped bool
	}{
		"held":    {},
		"skipped": {denied: []string{securityPolicyUsePermission}, skipped: true},
	} {
		if err := planningArmoredALB(t, tc.denied, tc.skipped); err != nil && strings.Contains(err.Error(), securityPolicyUsePermission) {
			t.Errorf("%s: Plan() = %v, want the policy trusted", name, err)
		}
	}
}

func TestADryDeployWhoseFrontPatchSetsACloudArmorPolicyTheDeployCredentialMayNotUseIsRefused(t *testing.T) {
	server := &googletest.Run{Iam: googletest.GrantedIAM()}
	server.Iam.ProjectDeniedPermissions = []string{securityPolicyUsePermission}
	p := servingTransformed(t, server, &patchingByKind{patches: map[string]transform.Patches{
		albresources.TransformKindLoadBalancer: {"notFoundBackend": {"securityPolicy": "projects/acme-prod/global/securityPolicies/front-armor"}},
	}})

	_, err := p.PlanPreflight(context.Background(), provider.DeployPreflight{Deploy: provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction}, Edge: albresources.Kind})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, "front-armor") || !strings.Contains(refused.Message, "project acme-prod") {
		t.Errorf("PlanPreflight() = %v, want a denied refusal naming the policy front-armor and project acme-prod", err)
	}
}
