package gcp

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/functions"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
)

func (p *Provider) release() *release.Release {
	return &release.Release{
		OpenClients:       p.OpenClients,
		ReadNames:         p.Names,
		Services:          p.Services(),
		Records:           p.KeyValues(),
		Cipher:            p.Cipher(),
		Components:        p.Components(),
		Topics:            p.TopicHooks(),
		Objects:           p.Objects(),
		CacheGrants:       cacheGrants,
		Offers:            p.offers(),
		Region:            p.Options.Region,
		ContainerEndpoint: p.ContainerEndpoint(),

		ContainerTagEndpoint: p.ContainerTagEndpoint(),
		GateInEnv:            p.gateInEnv,
		Transformed:          p.transforms != nil,
		SkipChecks:           p.SkipChecks,
	}
}

func (p *Provider) functionImages() functions.Images { return functions.NewImages(p.images.Based) }

func (p *Provider) computeHooks(hooks resources.Hooks) resources.Hooks {
	hooks.RecordsWorkers = true
	hooks.Functions = &resources.FunctionHooks{
		Provision: p.ProvisionFunctions,
		Remove:    p.RemoveFunctions,
		Shared:    &resources.SharedHooks[provider.Function]{Name: p.NameFunctions, RemoveRevisions: p.RemoveFunctionRevisions},
	}
	hooks.Containers = &resources.ContainerHooks{
		Provision: p.ProvisionContainers,
		Remove:    p.RemoveContainers,
		Shared:    &resources.SharedHooks[provider.AppContainer]{Name: p.NameContainers, RemoveRevisions: p.RemoveContainerRevisions},
	}
	hooks.Retention = &resources.ImageRetentionHooks{Reconcile: p.Services().ReconcileImages}
	return hooks
}
