package providerkit

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/costkit"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func (h *handlers) Shape(ctx context.Context, req *contractv1.ShapeRequest) (*costv1.ResourceSet, error) {
	provider, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	cost := provider.Hooks().Cost
	if cost == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this provider does not describe the resources a deploy would create"))
	}
	plan, err := buildDeployPlan(&contractv1.DeployRequest{
		Manifest:    unshipped(req.GetManifest()),
		Environment: req.GetEnvironment(),
		Edge:        req.GetEdge(),
	}, "")
	if err != nil {
		return nil, RefusalError(err)
	}
	resources, err := manifestResources(req.GetManifest())
	if err != nil {
		return nil, RefusalError(err)
	}
	features, err := RequiredFeatures(gate.Bootstrap.Catalogue(), frameworksOf(req.GetManifest()), string(gate.Edge))
	if err != nil {
		return nil, RefusalError(err)
	}
	set, err := cost.Shape(ctx, ShapeRequest{
		Plan:       plan,
		Edge:       gate.Edge,
		Features:   features,
		Resources:  resources,
		Functions:  shapedFunctions(req.GetManifest()),
		Transforms: h.session.transforms(),
	})
	if err != nil {
		return nil, RefusalError(err)
	}
	set.Source = CostSource
	return set, nil
}

const scanDeploymentID = "00000000000000000000000000000000"

func unshipped(manifest *contractv1.Manifest) *contractv1.Manifest {
	manifest = proto.Clone(manifest).(*contractv1.Manifest)
	for _, app := range manifest.GetApps() {
		if app.GetDeploymentId() == "" {
			app.DeploymentId = scanDeploymentID
		}
	}
	return manifest
}

func shapedFunctions(manifest *contractv1.Manifest) map[string][]FunctionSpec {
	specs := make(map[string][]FunctionSpec, len(manifest.GetApps()))
	for _, fn := range manifest.GetFunctions() {
		specs[fn.GetApp()] = append(specs[fn.GetApp()], FunctionSpec{
			Name:      fn.GetLogicalName(),
			Route:     fn.GetRouteId(),
			Handler:   fn.GetHandler(),
			Framework: frameworkOf(fn),
		})
	}
	return specs
}

func (h *handlers) Price(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	cost := provider.Hooks().Cost
	if cost == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this provider carries no rate card"))
	}
	estimated, err := cost.Estimate(ctx, req)
	var usage *costkit.UsageError
	if errors.As(err, &usage) {
		return nil, RefusalError(refusal.Refuse(refusal.CodeInvalid, "%s", err))
	}
	if err != nil {
		return nil, RefusalError(err)
	}
	return estimated, nil
}
