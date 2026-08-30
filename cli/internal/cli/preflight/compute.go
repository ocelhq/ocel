package preflight

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func ResolveComputes(cfg *projectconfig.Config, computes []string, provider string) (string, error) {
	if len(computes) == 0 {
		return "", fmt.Errorf(
			"%s names no compute it runs, so there is nothing for this project's apps to run on: a provider must name at least one in its preflight answer, and ocel will not guess one for it",
			named(provider),
		)
	}
	fallback := computes[0]

	for i := range cfg.Apps {
		app := &cfg.Apps[i]
		if app.Compute == "" {
			app.Compute = fallback
			continue
		}
		if !slices.Contains(computes, app.Compute) {
			return "", fmt.Errorf(
				"app %q asks for compute %q, which %s does not run — it runs %s: give %q a compute from that list, or deploy it to a provider that runs %q",
				app.Name, app.Compute, named(provider), strings.Join(quoted(computes), " and "), app.Name, app.Compute,
			)
		}
	}
	return fallback, nil
}

func named(provider string) string {
	if provider == "" {
		return "this project's provider"
	}
	return provider
}

func quoted(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprintf("%q", value))
	}
	return out
}
