package providerkit

import (
	"context"
	"errors"
	"slices"

	"connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func (h *handlers) Shape(ctx context.Context, req *contractv1.ShapeRequest) (*costv1.ResourceSet, error) {
	provider, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	shaper, shapes := provider.(Shaper)
	if !shapes {
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
	features, err := RequiredFeatures(gate.Bootstrapper.Catalogue(), runtimesOf(req.GetManifest()), string(gate.Edge))
	if err != nil {
		return nil, RefusalError(err)
	}
	for _, feature := range req.GetFeatures() {
		if !slices.Contains(features, feature) {
			features = append(features, feature)
		}
	}
	set, err := shaper.Shape(ctx, ShapeRequest{
		Plan:       plan,
		Edge:       gate.Edge,
		Features:   features,
		Resources:  resources,
		Functions:  shapedFunctions(provider, req.GetManifest()),
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
	for _, app := range manifest.GetApps() {
		if app.GetDeploymentId() == "" {
			app.DeploymentId = scanDeploymentID
		}
	}
	return manifest
}

func shapedFunctions(provider Provider, manifest *contractv1.Manifest) map[string][]FunctionSpec {
	url := true
	if addressed, says := provider.(ServesFunctionURLs); says {
		url = addressed.ServesFunctionURLs()
	}
	specs := make(map[string][]FunctionSpec, len(manifest.GetApps()))
	for _, fn := range manifest.GetFunctions() {
		specs[fn.GetApp()] = append(specs[fn.GetApp()], FunctionSpec{
			Name:    fn.GetLogicalName(),
			Route:   fn.GetRouteId(),
			Handler: fn.GetHandler(),
			Runtime: Runtime{Name: fn.GetRuntime().GetName(), Arch: fn.GetRuntime().GetArch()},
			URL:     url,
		})
	}
	return specs
}

func (h *handlers) Price(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	pricer, prices := provider.(Pricer)
	if !prices {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this provider carries no rate card"))
	}
	estimate, err := pricer.Price(ctx, req)
	if err != nil {
		return nil, RefusalError(err)
	}
	return estimate, nil
}
