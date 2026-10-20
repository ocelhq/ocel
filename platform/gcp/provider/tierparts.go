package gcp

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/bastion"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func tierParts() []bootstrap.RemovablePart {
	return []bootstrap.RemovablePart{
		bootstrap.Keys.Removable(),
		bootstrap.EdgeBucket.Removable(),
		bootstrap.CacheBucket.Removable(),
		{Part: provider.Part{Name: network.PartName, Summary: network.Summary}, Deployed: network.NewPart},
		albpart.Part,
		bootstrap.Realtime.Removable(),
		bootstrap.Topics.Removable(),
		envsource.Part,
		bastion.Part,
	}
}

func tierAccounts(registry component.Registry) []bootstrap.Account {
	return append([]bootstrap.Account{envsource.Account}, registry.Accounts()...)
}

func tierFronts() bootstrap.FrontParts { return cloudflareorigin.FrontParts }

type deployUse struct {
	deploy provider.DeploySpec
	front  edge.Kind
	usages []provider.AppUsage
	uses   []buildoutput.Uses
}

func (u deployUse) parts() []component.Part {
	var used []component.Part
	if u.seals() {
		used = append(used, component.NewPart(bootstrap.Keys))
	}
	if u.servesFromTheEdgeBucket() {
		used = append(used, component.NewPart(bootstrap.EdgeBucket))
	}
	if u.caches() {
		used = append(used, component.NewPart(bootstrap.CacheBucket))
	}
	return used
}

func (u deployUse) seals() bool {
	return u.deploy.Tier == environment.TierPreview || u.front == alb.Kind || isFronted(u.front) || u.opensValues()
}

func (u deployUse) opensValues() bool {
	return slices.ContainsFunc(u.deploy.Apps, opensSecrets) ||
		slices.ContainsFunc(u.usages, func(usage provider.AppUsage) bool { return len(usage.Grants) > 0 })
}

func (u deployUse) servesFromTheEdgeBucket() bool {
	return u.front == alb.Kind || isFronted(u.front) || slices.ContainsFunc(u.deploy.Apps, servesNext)
}

func (u deployUse) caches() bool {
	return slices.ContainsFunc(u.deploy.Apps, servesNext) ||
		slices.ContainsFunc(u.uses, func(uses buildoutput.Uses) bool { return uses.Revalidation })
}

func isFronted(front edge.Kind) bool {
	return slices.ContainsFunc(bootstrap.CredentialParts(), func(part provider.Part) bool { return slices.Contains(part.Edges, front) })
}

func servesNext(app provider.AppEntry) bool {
	return app.Manifest.GetFramework().GetName() == buildoutput.FrameworkNext
}

func opensSecrets(app provider.AppEntry) bool {
	return slices.ContainsFunc(app.Manifest.GetVariables(), func(variable *contractv1.ManifestVariable) bool {
		return variable.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
	})
}
