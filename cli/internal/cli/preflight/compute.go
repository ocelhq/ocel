package preflight

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

func ResolveComputesFromProvider(ctx context.Context, prov *providerclient.Provider, cfg *projectconfig.Config) (string, error) {
	var resp *contractv1.PreflightResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.Preflight(ctx, &contractv1.PreflightRequest{Edge: cfg.EdgeSelection()})
		return err
	})
	if err != nil {
		return "", err
	}
	return ResolveComputes(cfg, resp.GetComputes(), prov.Name())
}

func ResolveComputes(cfg *projectconfig.Config, computes []string, vendor string) (string, error) {
	if len(computes) == 0 {
		return "", fmt.Errorf(
			"%s names no compute it runs, so there is nothing for this project's apps to run on: a provider must name at least one in its preflight answer, and ocel will not guess one for it",
			vendor,
		)
	}
	for _, compute := range computes {
		if provider.KnownCompute(compute) {
			continue
		}
		return "", fmt.Errorf(
			"%s names %q among the computes it runs, and ocel knows no such compute — it knows %s: upgrade ocel if %q is newer than this build, or pin a provider version this ocel understands",
			vendor, compute, runui.Quoted(provider.ComputeNames(provider.Computes())), compute,
		)
	}

	fallback := computes[0]
	resolved := make([]string, len(cfg.Apps))
	for i, app := range cfg.Apps {
		if app.Compute == "" {
			resolved[i] = fallback
			continue
		}
		if !slices.Contains(computes, app.Compute) {
			return "", fmt.Errorf(
				"app %q asks for compute %q, which %s does not run — it runs %s: give %q a compute from that list, or deploy it to a provider that runs %q",
				app.Name, app.Compute, vendor, runui.Quoted(computes), app.Name, app.Compute,
			)
		}
		resolved[i] = app.Compute
	}
	for i, app := range cfg.Apps {
		if err := containerOnly(app, resolved[i]); err != nil {
			return "", err
		}
	}
	for i := range cfg.Apps {
		cfg.Apps[i].Compute = resolved[i]
		if resolved[i] == string(provider.ComputeContainer) && cfg.Apps[i].Framework.Detected {
			cfg.Apps[i].Framework = projectconfig.Framework{Arch: cfg.Apps[i].Framework.Arch}
		}
	}
	return fallback, nil
}

func containerOnly(app projectconfig.App, compute string) error {
	if compute == string(provider.ComputeContainer) {
		if app.Framework.Name != "" && !app.Framework.Detected {
			return fmt.Errorf(
				"app %q declares framework %q, and it runs on %q compute, which runs the image it is given: a framework names what a serverless app's functions are built with and nothing else — give %q `compute: \"serverless\"`, or remove its `framework`",
				app.Name, app.Framework.Name, compute, app.Name,
			)
		}
		return nil
	}
	if app.Build != nil {
		return fmt.Errorf(
			"app %q configures a `build`, and it runs on %q compute, which builds no image: `build` configures a container image and nothing else — give %q `compute: \"container\"`, or remove its `build`",
			app.Name, compute, app.Name,
		)
	}
	if app.Health != nil {
		return fmt.Errorf(
			"app %q configures a `health` check, and it runs on %q compute, which runs no process to probe: `health` gates a container release and nothing else — give %q `compute: \"container\"`, or remove its `health`",
			app.Name, compute, app.Name,
		)
	}
	return nil
}
