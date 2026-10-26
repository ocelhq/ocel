package release

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/postgres"
)

type ServicePatch struct {
	resource string
	surface  serviceSurface
	result   transform.Result
}

func (s ServicePatch) Apply(desired *runv2.GoogleCloudRunV2Service) error {
	patched, err := s.surface.Patch(s.resource, *desired, s.result.Patches[serviceSurfaceName], s.result.Tags, surface.Recorded{})
	if err != nil {
		return err
	}
	*desired = patched.Desired
	return nil
}

type serviceKey struct {
	kind string
	name string
}

type AppPatches struct {
	services map[serviceKey]ServicePatch
}

func (a AppPatches) For(kind, name string) ServicePatch {
	return a.services[serviceKey{kind: kind, name: name}]
}

func (p *Release) ReadAppPatches(ctx context.Context, project string, spec provider.StackSpec) (AppPatches, error) {
	evaluated, found := surface.ReadEvaluated(ctx, spec.Ref)
	if !found && p.Transformed {
		return AppPatches{}, fmt.Errorf("the transforms for %s were not evaluated before app %s was released", spec.Ref.Name, spec.App.App)
	}
	candidates := TransformCandidates(spec)
	ceiling := postgres.ConnectionBudgetOf(spec, len(candidates)).Ceiling()
	at := servicePlacement{project: project, region: p.Region}
	patches := AppPatches{services: map[serviceKey]ServicePatch{}}
	for _, candidate := range candidates {
		patch := ServicePatch{
			resource: candidate.Type + " " + candidate.Name,
			surface:  newServiceSurface(at, candidate.Type != TransformWorker),
			result:   evaluated.ForApp(candidate.App, candidate.Type, candidate.Name),
		}
		probe := probeService()
		if err := patch.Apply(probe); err != nil {
			return AppPatches{}, err
		}
		if err := refuseOverBudget(patch.resource, probe.Template.Scaling, ceiling); err != nil {
			return AppPatches{}, err
		}
		patches.services[serviceKey{kind: candidate.Type, name: candidate.Name}] = patch
	}
	return patches, nil
}

func (p *Release) RefuseAppPatches(ctx context.Context, spec provider.StackSpec) error {
	names, err := p.ReadNames(ctx)
	if err != nil {
		return err
	}
	_, err = p.ReadAppPatches(ctx, names.Project(), spec)
	return err
}

func ReadMemory(served run.Serving) (int, error) {
	desired, err := run.ServiceOf(served)
	if err != nil {
		return 0, err
	}
	return run.MemoryMB(desired.Template.Containers[0].Resources.Limits["memory"]), nil
}

func probeService() *runv2.GoogleCloudRunV2Service {
	return &runv2.GoogleCloudRunV2Service{Template: &runv2.GoogleCloudRunV2RevisionTemplate{
		Containers: []*runv2.GoogleCloudRunV2Container{{Resources: &runv2.GoogleCloudRunV2ResourceRequirements{}}},
		Scaling:    &runv2.GoogleCloudRunV2RevisionScaling{},
	}}
}

func refuseOverBudget(resource string, scaling *runv2.GoogleCloudRunV2RevisionScaling, ceiling int) error {
	asked := max(scaling.MinInstanceCount, scaling.MaxInstanceCount)
	if ceiling == 0 || asked <= int64(ceiling) {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform scales %s to %d instances, and its share of the app's postgres connections fits %d: patch %s.template.scaling to at most %d",
		resource, asked, ceiling, serviceSurfaceName, ceiling)
}

func PriceService(project, region string, served run.Serving, app, kind, name string, evaluated surface.Evaluated) (map[string]any, []string, error) {
	service := newServiceSurface(servicePlacement{project: project, region: region}, kind != TransformWorker)
	known, unknown := service.SplitOutputs(evaluated.ForApp(app, kind, name).Patches[serviceSurfaceName])
	patch := ServicePatch{
		resource: kind + " " + name,
		surface:  service,
		result:   transform.Result{Patches: transform.Patches{serviceSurfaceName: known}},
	}
	served.Patch = patch.Apply
	desired, err := run.ServiceOf(served)
	if err != nil {
		return nil, nil, err
	}
	var priced []string
	for _, path := range slices.Sorted(maps.Keys(pricedPaths)) {
		if slices.ContainsFunc(unknown, func(unset string) bool { return path == unset || strings.HasPrefix(path, unset+".") }) {
			priced = append(priced, pricedPaths[path])
		}
	}
	return run.PricedService(desired), priced, nil
}

var pricedPaths = map[string]string{
	"template.containers.resources.limits.cpu":    "template.containers.0.resources.limits.cpu",
	"template.containers.resources.limits.memory": "template.containers.0.resources.limits.memory",
	"template.containers.resources.cpuIdle":       "template.containers.0.resources.cpu_idle",
	"template.scaling.minInstanceCount":           "template.scaling.min_instance_count",
	"template.scaling.maxInstanceCount":           "template.scaling.max_instance_count",
}
