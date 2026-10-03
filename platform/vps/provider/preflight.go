package vps

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	if err := refuseScaledContainers(pre.Deploy); err != nil {
		return err
	}
	if err := refuseEphemeralInfra(pre); err != nil {
		return err
	}
	if err := p.host.CheckEngine(ctx); err != nil {
		return err
	}
	if err := p.host.RefuseDisagreeingFront(ctx, pre.Deploy.Tier); err != nil {
		return err
	}
	if err := p.refuseStaleAgent(ctx, pre); err != nil {
		return err
	}
	if err := p.refuseStaleGateway(ctx, pre); err != nil {
		return err
	}
	if pre.Deploy.Tier == environment.TierPreview && !p.options.PerHostnamePreviewCertificates {
		if err := p.host.RefusePerHostnamePreviewCertificates(ctx, pre.PreviewBaseDomain); err != nil {
			return err
		}
	}
	if err := refusing([]error{
		p.host.CheckDisk(ctx, repositories(pre.Deploy)),
		p.host.CheckProxy(ctx),
		p.host.ProxyOwnsServingPorts(ctx),
	}); err != nil || pre.Edge != edge.None {
		return err
	}
	said, err := p.readServingPortsClosedFromOutside(ctx)
	if said != "" && pre.Progress != nil {
		pre.Progress.Warn(said)
	}
	return err
}

func isQueued(resource provider.Resource) bool {
	return resource.Type == provider.BindingTopic || resource.Type == provider.BindingTask
}

func isRealtime(resource provider.Resource) bool { return resource.Type == provider.BindingRealtime }

func refuseEphemeralInfra(pre provider.DeployPreflight) error {
	if !pre.Deploy.Infra.IsZero() {
		return nil
	}
	for _, declared := range pre.Resources {
		name := chooseDeclaredName(declared.Declared, declared.Name)
		switch {
		case isQueued(declared):
			return refusal.Refuse(refusal.CodeInvalid,
				"this ephemeral preview declares %s %s, and a box runs topics and tasks only on the queue of an environment with an infra stack of its own: deploy it as a persistent preview, or leave the %s out of it",
				declared.Type, name, declared.Type)
		case isRealtime(declared):
			return refusal.Refuse(refusal.CodeInvalid,
				"this ephemeral preview declares realtime %s, and a box serves realtime only from the gateway of an environment with an infra stack of its own: deploy it as a persistent preview, or leave the realtime out of it",
				name)
		}
	}
	return nil
}

func (p *Provider) refuseStaleAgent(ctx context.Context, pre provider.DeployPreflight) error {
	if !slices.ContainsFunc(pre.Resources, isQueued) {
		return nil
	}
	current, err := p.host.IsAgentCurrent(ctx)
	if err != nil || current {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"this box's agent at %s predates this ocel and runs no topic or task, and this deploy declares them\nRun `%s` and deploy again",
		host.LiveBinary, provider.BootstrapCommand(pre.Deploy.Tier))
}

func (p *Provider) refuseStaleGateway(ctx context.Context, pre provider.DeployPreflight) error {
	if !slices.ContainsFunc(pre.Resources, isRealtime) {
		return nil
	}
	current, err := p.host.IsRealtimeCurrent(ctx)
	if err != nil || current {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"this box's realtime gateway at %s predates this ocel, and this deploy declares realtime\nRun `%s` and deploy again",
		host.RealtimeBinary, provider.BootstrapCommand(pre.Deploy.Tier))
}

var oneInstance = provider.Instances{Min: 1, Max: 1}

func refuseScaledContainers(spec provider.DeploySpec) error {
	for _, app := range spec.Apps {
		if app.Compute() != provider.ComputeContainer || app.Instances == oneInstance {
			continue
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s asks for minInstances %d and maxInstances %d, and a box runs one container per app behind its proxy: set both to 1, or leave them off",
			app.App, app.Instances.Min, app.Instances.Max)
	}
	return nil
}

func repositories(spec provider.DeploySpec) []string {
	var named []string
	for _, app := range spec.Apps {
		repository, ok := host.Repository(app.Image)
		if !ok || slices.Contains(named, repository) {
			continue
		}
		named = append(named, repository)
	}
	return named
}

func refusing(found []error) error {
	var said []string
	for _, err := range found {
		if err == nil {
			continue
		}
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			return err
		}
		said = append(said, refused.Message)
	}
	switch len(said) {
	case 0:
		return nil
	case 1:
		return refusal.Refuse(refusal.CodeNotReady, "%s", said[0])
	default:
		return refusal.Refuse(refusal.CodeNotReady,
			"this box is not ready for a deploy:\n\n%s", strings.Join(said, "\n\n"))
	}
}
