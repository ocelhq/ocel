package manifest

import (
	"slices"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type InfraInput struct {
	Project   *project.Project
	Tier      environmentv1.Tier
	Resources []declaration.Resource

	WorkerCeilings []provider.WorkerCeiling
}

func AssembleInfra(input InfraInput) (*contractv1.Manifest, error) {
	cfg := input.Project
	declarations := declaredResources(cfg.Dir, input.Resources)
	topics, workers, err := placeConsumers(appsOf(cfg.Dir, cfg.Apps, nil, nil), declarations, input.WorkerCeilings)
	if err != nil {
		return nil, err
	}
	resources, _, err := manifestResources(declarations, topics, map[string]string{})
	if err != nil {
		return nil, err
	}
	if err := bindBindings(resources, bindingsOf(cfg.BindingsFor(input.Tier))); err != nil {
		return nil, err
	}
	return &contractv1.Manifest{
		Slug:      cfg.Slug,
		Resources: resources,
		Domains:   tierDomains(servedDomains(cfg)),
		Workers:   workers,
	}, nil
}

func servedDomains(cfg *project.Project) project.Domains {
	domains := project.Domains{Preview: cfg.Domains.Preview, Production: slices.Clone(cfg.Domains.Production)}
	for _, app := range cfg.Apps {
		domains.Production = append(domains.Production, app.ProductionDomains...)
	}
	return domains
}
