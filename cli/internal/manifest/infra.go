package manifest

import (
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

func AssembleInfra(in InfraInput) (*contractv1.Manifest, error) {
	cfg := in.Project
	declarations := declaredResources(cfg.Dir, in.Resources)
	topics, workers, err := placeConsumers(appsOf(cfg.Dir, cfg.Apps, nil, nil), declarations, in.WorkerCeilings)
	if err != nil {
		return nil, err
	}
	resources, _, err := manifestResources(declarations, topics, map[string]string{})
	if err != nil {
		return nil, err
	}
	if err := bindBindings(resources, bindingsOf(cfg.BindingsFor(in.Tier))); err != nil {
		return nil, err
	}
	return &contractv1.Manifest{
		Slug:      cfg.Slug,
		Resources: resources,
		Domains:   tierDomains(cfg.Domains),
		Workers:   workers,
	}, nil
}
