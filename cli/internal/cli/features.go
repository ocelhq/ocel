package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const (
	featureISR               = "isr"
	featureImageOptimization = "image-optimization"
	featureCloudflareEdge    = "cloudflare-edge"

	allFeatures        = "all"
	noFeatures         = "none"
	nextFramework      = "next"
	cloudflareEdgeKind = "cloudflare"
)

func configuredFeatures(cfg *projectconfig.Config) []string {
	var required []string
	if string(cfg.EdgeKind()) == cloudflareEdgeKind {
		required = append(required, featureCloudflareEdge, featureISR)
	}
	for _, app := range cfg.Apps {
		required = append(required, frameworkFeatures(app.Framework)...)
	}
	slices.Sort(required)
	return slices.Compact(required)
}

func requiredFeatures(cfg *projectconfig.Config, manifest *deploymentsv1.Manifest) []string {
	required := configuredFeatures(cfg)
	for _, app := range manifest.GetApps() {
		required = append(required, frameworkFeatures(app.GetFramework())...)
	}
	for _, fn := range manifest.GetFunctions() {
		required = append(required, frameworkFeatures(fn.GetFramework())...)
	}
	slices.Sort(required)
	return slices.Compact(required)
}

func frameworkFeatures(framework string) []string {
	if framework != nextFramework {
		return nil
	}
	return []string{featureISR, featureImageOptimization}
}

func catalogueNames(catalogue []*deploymentsv1.Feature) []string {
	names := make([]string, 0, len(catalogue))
	for _, f := range catalogue {
		names = append(names, f.GetName())
	}
	return names
}

func enabledFeatures(catalogue []*deploymentsv1.Feature) []string {
	var enabled []string
	for _, f := range catalogue {
		if f.GetEnabled() {
			enabled = append(enabled, f.GetName())
		}
	}
	return enabled
}

func inCatalogueOrder(catalogue []*deploymentsv1.Feature, chosen []string) []string {
	var ordered []string
	for _, f := range catalogue {
		if slices.Contains(chosen, f.GetName()) {
			ordered = append(ordered, f.GetName())
		}
	}
	return ordered
}

func withDependencies(catalogue []*deploymentsv1.Feature, chosen []string) []string {
	pulled := slices.Clone(chosen)
	for grew := true; grew; {
		grew = false
		for _, f := range catalogue {
			if !slices.Contains(pulled, f.GetName()) {
				continue
			}
			for _, dep := range f.GetDependsOn() {
				if !slices.Contains(pulled, dep) {
					pulled, grew = append(pulled, dep), true
				}
			}
		}
	}
	return inCatalogueOrder(catalogue, pulled)
}

func parseFeatureFlag(raw string, catalogue []*deploymentsv1.Feature) ([]string, error) {
	switch strings.TrimSpace(raw) {
	case allFeatures:
		return catalogueNames(catalogue), nil
	case noFeatures, "":
		return nil, nil
	}

	var chosen []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(catalogueNames(catalogue), name) {
			return nil, fmt.Errorf("this provider has no bootstrap feature named %q; it offers %s, or %s for all of them and %s for none",
				name, strings.Join(catalogueNames(catalogue), ", "), allFeatures, noFeatures)
		}
		if !slices.Contains(chosen, name) {
			chosen = append(chosen, name)
		}
	}
	if err := unmetDependency(catalogue, chosen); err != nil {
		return nil, err
	}
	return inCatalogueOrder(catalogue, chosen), nil
}

func unmetDependency(catalogue []*deploymentsv1.Feature, chosen []string) error {
	for _, f := range catalogue {
		if !slices.Contains(chosen, f.GetName()) {
			continue
		}
		for _, dep := range f.GetDependsOn() {
			if slices.Contains(chosen, dep) {
				continue
			}
			return fmt.Errorf("%s needs %s, which this set leaves out; pass --features %s",
				f.GetName(), dep, strings.Join(withDependencies(catalogue, chosen), ","))
		}
	}
	return nil
}

func droppedFeatures(enabled, requested []string) []string {
	var dropped []string
	for _, name := range enabled {
		if !slices.Contains(requested, name) {
			dropped = append(dropped, name)
		}
	}
	return dropped
}

func dependentProjects(catalogue []*deploymentsv1.Feature, dropped []string) []string {
	var projects []string
	for _, f := range catalogue {
		if !slices.Contains(dropped, f.GetName()) {
			continue
		}
		for _, project := range f.GetDependents() {
			if !slices.Contains(projects, project) {
				projects = append(projects, project)
			}
		}
	}
	slices.Sort(projects)
	return projects
}

func pickFeatures(ctx context.Context, catalogue []*deploymentsv1.Feature, stdout io.Writer, stdin io.Reader) ([]string, bool, error) {
	chosen := enabledFeatures(catalogue)
	for {
		fmt.Fprintln(stdout, "Features this bootstrap carries:")
		for i, f := range catalogue {
			mark := " "
			if slices.Contains(chosen, f.GetName()) {
				mark = "x"
			}
			fmt.Fprintf(stdout, "  %d) [%s] %s", i+1, mark, f.GetName())
			if summary := f.GetSummary(); summary != "" {
				fmt.Fprintf(stdout, " — %s", summary)
			}
			fmt.Fprintln(stdout)
		}
		fmt.Fprint(stdout, "Numbers to toggle, comma separated, or Enter to take this set: ")

		line, err := readLine(ctx, stdin)
		if err != nil {
			if err == io.EOF {
				return nil, false, nil
			}
			return nil, false, err
		}
		if strings.TrimSpace(line) == "" {
			return withDependencies(catalogue, chosen), true, nil
		}
		toggled, err := toggleFeatures(catalogue, chosen, line)
		if err != nil {
			fmt.Fprintln(stdout, err)
			continue
		}
		chosen = toggled
	}
}

func toggleFeatures(catalogue []*deploymentsv1.Feature, chosen []string, line string) ([]string, error) {
	next := slices.Clone(chosen)
	for _, field := range strings.Split(line, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		index, err := strconv.Atoi(field)
		if err != nil || index < 1 || index > len(catalogue) {
			return nil, fmt.Errorf("%q is not one of 1-%d", field, len(catalogue))
		}
		name := catalogue[index-1].GetName()
		if at := slices.Index(next, name); at >= 0 {
			next = slices.Delete(next, at, at+1)
			continue
		}
		next = append(next, name)
	}
	return inCatalogueOrder(catalogue, next), nil
}
