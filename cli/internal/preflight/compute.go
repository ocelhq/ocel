package preflight

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func ResolveComputesFromProvider(ctx context.Context, prov *providerclient.Provider, cfg *project.Project) (*project.Project, error) {
	var resp *contractv1.PreflightResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.Preflight(ctx, &contractv1.PreflightRequest{Edge: cfg.EdgeSelection()})
		return err
	})
	if err != nil {
		return nil, err
	}
	return cfg.ResolveComputes(resp.GetComputes(), prov.Name())
}

func ContainerArchs(ctx context.Context, prov *providerclient.Provider, resolved *project.Project, announced map[string]string) (map[string]string, error) {
	containers := Containers(resolved)
	missing := false
	for _, container := range containers {
		if _, ok := announced[container.GetApp()]; !ok {
			missing = true
		}
	}
	if !missing {
		return announced, nil
	}
	var resp *contractv1.PreflightResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.Preflight(ctx, &contractv1.PreflightRequest{Edge: resolved.EdgeSelection(), Containers: containers})
		return err
	})
	if err != nil {
		return nil, err
	}
	return resp.GetContainerArchs(), nil
}
