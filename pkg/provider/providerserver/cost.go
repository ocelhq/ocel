package providerserver

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/pricing"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (h *handlers) Shape(ctx context.Context, req *contractv1.ShapeRequest) (*costv1.ResourceSet, error) {
	p, readiness, err := h.readiness(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	cost := p.Hooks().Cost
	if cost == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this provider does not describe the resources a deploy would create"))
	}
	spec, err := buildDeploySpec(&contractv1.DeployRequest{
		Manifest:    unshipped(req.GetManifest()),
		Environment: req.GetEnvironment(),
		Edge:        req.GetEdge(),
	}, scanPromotionID)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resources, err := manifestResources(req.GetManifest())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	parts := bootstrapplan.RequiredParts(readiness.Bootstrap.Parts(), readiness.Edge)
	set, err := cost.Shape(ctx, provider.ShapeRequest{
		Deploy:     spec,
		Edge:       readiness.Edge,
		Parts:      parts,
		Resources:  resources,
		Functions:  shapedFunctions(req.GetManifest()),
		Usages:     shapedUsages(spec, req.GetManifest(), resources),
		Transforms: h.session.transforms(),
	})
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	set.Source = provider.CostSource
	return set, nil
}

const (
	scanBuildID     = "00000000000000000000000000000000"
	scanPromotionID = "shape"
)

func unshipped(manifest *contractv1.Manifest) *contractv1.Manifest {
	manifest = proto.Clone(manifest).(*contractv1.Manifest)
	for _, app := range manifest.GetApps() {
		if app.GetBuildId() == "" {
			app.BuildId = scanBuildID
		}
	}
	return manifest
}

func shapedFunctions(manifest *contractv1.Manifest) map[string][]provider.FunctionSpec {
	specs := make(map[string][]provider.FunctionSpec, len(manifest.GetApps()))
	for _, app := range manifest.GetApps() {
		for _, fn := range app.GetServerless().GetFunctions() {
			specs[app.GetName()] = append(specs[app.GetName()], functionSpecOf(fn))
		}
	}
	return specs
}

func shapedUsages(spec provider.DeploySpec, manifest *contractv1.Manifest, resources []provider.Resource) []provider.AppUsage {
	usages := make([]provider.AppUsage, 0, len(spec.Apps))
	for _, entry := range spec.Apps {
		used := boundNamesUsedBy(manifest, resources, entry.App)
		usages = append(usages, provider.AppUsage{App: entry.App, Resources: usedResources(used, resources)})
	}
	return usages
}

func (h *handlers) Price(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	cost := p.Hooks().Cost
	if cost == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this provider ships no rate card"))
	}
	estimated, err := cost.Estimate(ctx, req)
	var usage *pricing.UsageError
	if errors.As(err, &usage) {
		return nil, provider.RefusalError(refusal.Refuse(refusal.CodeInvalid, "%s", err))
	}
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return estimated, nil
}
