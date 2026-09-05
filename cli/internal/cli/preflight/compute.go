package preflight

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func ResolveComputes(cfg *projectconfig.Config, computes []string, provider string) (string, error) {
	if len(computes) == 0 {
		return "", fmt.Errorf(
			"%s names no compute it runs, so there is nothing for this project's apps to run on: a provider must name at least one in its preflight answer, and ocel will not guess one for it",
			provider,
		)
	}
	for _, compute := range computes {
		if providerkit.KnownCompute(compute) {
			continue
		}
		return "", fmt.Errorf(
			"%s names %q among the computes it runs, and ocel knows no such compute — it knows %s: upgrade ocel if %q is newer than this build, or pin a provider version this ocel understands",
			provider, compute, runui.Quoted(providerkit.ComputeNames(providerkit.Computes())), compute,
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
				app.Name, app.Compute, provider, runui.Quoted(computes), app.Name, app.Compute,
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
	}
	return fallback, nil
}

func RunsAContainer(cfg *projectconfig.Config, computes []string) bool {
	fallback := ""
	if len(computes) > 0 {
		fallback = computes[0]
	}
	if len(cfg.Apps) == 0 {
		return fallback == string(providerkit.ComputeContainer)
	}
	return slices.ContainsFunc(cfg.Apps, func(app projectconfig.App) bool {
		if app.Compute == "" {
			return fallback == string(providerkit.ComputeContainer)
		}
		return app.Compute == string(providerkit.ComputeContainer)
	})
}

func containerOnly(app projectconfig.App, compute string) error {
	if compute == string(providerkit.ComputeContainer) {
		if app.Runtime.Name != "" {
			return fmt.Errorf(
				"app %q declares runtime %q, and it runs on %q compute, which runs the image it is given: a runtime names what a serverless app's functions run on and nothing else — give %q `compute: \"serverless\"`, or remove its `runtime`",
				app.Name, app.Runtime.Name, compute, app.Name,
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
			"app %q configures a `health` check, and it runs on %q compute, which stands up no process to probe: `health` gates a container release and nothing else — give %q `compute: \"container\"`, or remove its `health`",
			app.Name, compute, app.Name,
		)
	}
	return nil
}
