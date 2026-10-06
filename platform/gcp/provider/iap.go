package gcp

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"google.golang.org/api/googleapi"
	iap "google.golang.org/api/iap/v1"
	serviceidentities "google.golang.org/api/serviceusage/v1beta1"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	proxyAPI       = "iap.googleapis.com"
	viewerRole     = "roles/iap.httpsResourceAccessor"
	iapAgentDomain = "@gcp-sa-iap.iam.gserviceaccount.com"
	iapResource    = "projects/%d/iap_web/cloud_run-%s/services/%s"
)

var memberKinds = []string{"user:", "group:", "domain:", "serviceAccount:", "principal:", "principalSet:"}

func (p *Provider) servesBehindIAP(spec provider.StackSpec) bool {
	return p.gatesBehindIAP(spec.Ref.Tier, factsOf(spec.Edge))
}

func (p *Provider) gatesBehindIAP(tier environment.Tier, front edge.Facts) bool {
	return tier == environment.TierPreview && !front.ShieldsOrigin && !p.emulated()
}

func refuseViewersWithoutKind(viewers []string) error {
	for _, viewer := range viewers {
		if !slices.ContainsFunc(memberKinds, func(kind string) bool { return strings.HasPrefix(viewer, kind) && len(viewer) > len(kind) }) {
			return refusal.Refuse(refusal.CodeInvalid,
				"previewViewers names %q, and IAM takes a member only with its kind in front: write it as user:%s, group:%s or domain:%s",
				viewer, viewer, viewer, viewer)
		}
	}
	return nil
}

func (p *Provider) grantViewers(ctx context.Context, c *clients, service, account string, progress progress.Log) error {
	agent, err := c.ReadServiceAgent(ctx, iapAgentDomain)
	if err != nil {
		return err
	}
	if err := p.grantInvoker(ctx, c, service, agent); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s is a preview behind Identity-Aware Proxy, and the proxy reaches it as its service agent, which could not be granted run.invoker on it: %v.\n"+
				"Run `ocel bootstrap preview`, which creates the agent, then deploy again",
			service, err)
	}
	viewers := p.options.PreviewViewers
	err = p.setViewers(ctx, c, service, admittedMembers(account, viewers))
	if answeredCode(err) == http.StatusForbidden {
		return refusal.Refuse(refusal.CodeDenied,
			"%s is a preview behind Identity-Aware Proxy, and this credential may not set who the proxy lets through to it: %v.\n"+
				"Grant it %s, as `ocel permissions deploy` prints, then deploy again",
			service, err, proxyViewersGrant(c.Names, c.region))
	}
	if err != nil {
		return err
	}
	if len(viewers) == 0 {
		ensureProgress(progress).Warn("Cloud Run service " + service + " is a preview behind Identity-Aware Proxy, and previewViewers names nobody, " +
			"so nobody may open it: name the people or groups who may in the gcp provider's previewViewers")
	}
	return nil
}

func admittedMembers(account string, viewers []string) []string {
	return append(slices.Clone(viewers), "serviceAccount:"+account)
}

func allowWarming(ctx context.Context, c *clients, app, account string, progress progress.Log) {
	principal, err := c.Principal(ctx)
	if err == nil {
		name := strings.TrimSuffix(account, "@"+c.project+accountDomain)
		err = c.bindAccountRole(ctx, name, tokenCreatorRole, memberOf(principal), true)
	}
	if err != nil {
		ensureProgress(progress).Warn("a promotion cannot warm app " + app + " behind Identity-Aware Proxy until the credential may sign tokens as " + account + ": " + err.Error())
	}
}

func (p *Provider) setViewers(ctx context.Context, c *clients, service string, viewers []string) error {
	proxy, err := c.IAP()
	if err != nil {
		return err
	}
	number, err := c.ReadProjectNumber(ctx)
	if err != nil {
		return err
	}
	resource := fmt.Sprintf(iapResource, number, c.region, service)
	return p.retryWrite(ctx, "let the preview viewers through to "+service, func() error {
		current, err := attempted(ctx, func(call ...googleapi.CallOption) (*iap.Policy, error) {
			return proxy.V1.GetIamPolicy(resource, &iap.GetIamPolicyRequest{}).Context(ctx).Do(call...)
		})
		if err != nil {
			return fmt.Errorf("read who Identity-Aware Proxy lets through to %s: %w", service, err)
		}
		_, err = attempted(ctx, func(call ...googleapi.CallOption) (*iap.Policy, error) {
			return proxy.V1.SetIamPolicy(resource, &iap.SetIamPolicyRequest{Policy: viewerPolicy(current, viewers)}).Context(ctx).Do(call...)
		})
		if err != nil {
			return fmt.Errorf("let the preview viewers through to %s: %w", service, err)
		}
		return nil
	})
}

func viewerPolicy(current *iap.Policy, viewers []string) *iap.Policy {
	policy := &iap.Policy{Etag: current.Etag, Version: current.Version}
	for _, binding := range current.Bindings {
		if binding.Role != viewerRole || binding.Condition != nil {
			policy.Bindings = append(policy.Bindings, binding)
		}
	}
	if len(viewers) > 0 {
		policy.Bindings = append(policy.Bindings, &iap.Binding{Role: viewerRole, Members: slices.Sorted(slices.Values(viewers))})
	}
	return policy
}

func (b bootstrap) makeServiceAgent(ctx context.Context, api string) error {
	number, err := b.clients.ReadProjectNumber(ctx)
	if err != nil {
		return err
	}
	identities, err := b.clients.ServiceIdentities()
	if err != nil {
		return err
	}
	started, err := attempted(ctx, func(call ...googleapi.CallOption) (*serviceidentities.Operation, error) {
		return identities.Services.GenerateServiceIdentity(fmt.Sprintf("projects/%d/services/%s", number, api)).Context(ctx).Do(call...)
	})
	if answeredCode(err) == http.StatusForbidden {
		return refusal.Refuse(refusal.CodeDenied,
			"create the service agent of %s, which Identity-Aware Proxy calls a preview with no edge in front as: %v.\n"+
				"Google documents no permission for creating one, so run this bootstrap as a principal that may, such as an owner of project %s",
			api, err, b.clients.project)
	}
	if err != nil {
		return fmt.Errorf("create the service agent of %s: %w", api, err)
	}
	finished, err := until(ctx, "Service Usage to create the service agent of "+api, func() (*serviceidentities.Operation, error) {
		if started.Done || started.Name == "" {
			return started, nil
		}
		return attempted(ctx, func(call ...googleapi.CallOption) (*serviceidentities.Operation, error) {
			return identities.Operations.Get(started.Name).Context(ctx).Do(call...)
		})
	}, func(op *serviceidentities.Operation) bool { return op != nil && (op.Done || op.Name == "") })
	if err != nil {
		return err
	}
	if finished.Error != nil {
		return fmt.Errorf("Service Usage refused to create the service agent of %s: %s", api, finished.Error.Message)
	}
	return nil
}
