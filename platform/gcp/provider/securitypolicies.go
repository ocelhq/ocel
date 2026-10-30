package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const securityPolicyUsePermission = "compute.securityPolicies.use"

func (p *Provider) refuseUnusableSecurityPolicies(ctx context.Context, evaluated transform.Result) error {
	policies := alb.SecurityPolicies(evaluated)
	if p.SkipChecks || len(policies) == 0 {
		return nil
	}
	c, err := p.OpenClients(ctx)
	if err != nil {
		return err
	}
	service, err := c.Projects()
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, policy := range policies {
		project := securityPolicyProject(policy, c.Project())
		permitted, asked := held[project]
		if !asked {
			answer, err := googleretry.Attempted(ctx, service.Projects.TestIamPermissions(project,
				&cloudresourcemanager.TestIamPermissionsRequest{Permissions: []string{securityPolicyUsePermission}}).Context(ctx).Do)
			if err != nil {
				return fmt.Errorf("ask project %s whether this credential may attach Cloud Armor policies in it: %w", project, err)
			}
			permitted = slices.Contains(answer.Permissions, securityPolicyUsePermission)
			held[project] = permitted
		}
		if !permitted {
			return refusal.Refuse(refusal.CodeDenied,
				"a transform attaches the Cloud Armor policy %s to the alb, and the deploy credential may not use it: grant the deploy credential %s on project %s",
				policy, securityPolicyUsePermission, project)
		}
	}
	return nil
}

func (p *Provider) refuseUnusableFrontPolicies(ctx context.Context, pre provider.DeployPreflight) error {
	evaluated, err := p.evaluateFront(ctx, pre.Deploy.Tier)
	if err != nil {
		return err
	}
	return p.refuseUnusableSecurityPolicies(ctx, evaluated)
}

func securityPolicyProject(policy, own string) string {
	_, rest, found := strings.Cut(policy, "projects/")
	if !found {
		return own
	}
	project, _, _ := strings.Cut(rest, "/")
	return project
}
