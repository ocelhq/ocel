package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"charm.land/huh/v2"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	allFeatures = "all"
	noFeatures  = "none"
)

func chooseFeatures(ctx context.Context, opts Options, tier environmentv1.Tier, catalogue []*contractv1.Feature, interactive bool, stdout io.Writer) ([]string, bool, error) {
	if opts.FeaturesDeclared {
		requested, err := parseFeatureFlag(opts.Features, catalogue)
		return requested, err == nil, err
	}
	if !interactive {
		return enabledFeatures(catalogue), true, nil
	}
	return pickFeatures(ctx, tier, catalogue, stdout)
}

func pickFeatures(ctx context.Context, tier environmentv1.Tier, catalogue []*contractv1.Feature, stdout io.Writer) ([]string, bool, error) {
	if len(catalogue) == 0 {
		return nil, true, nil
	}

	printCatalogue(stdout, tier, catalogue)

	enabled := enabledFeatures(catalogue)
	options := make([]huh.Option[string], 0, len(catalogue))
	for _, f := range catalogue {
		options = append(options, huh.NewOption(f.GetName(), f.GetName()).Selected(slices.Contains(enabled, f.GetName())))
	}

	chosen := slices.Clone(enabled)
	field := huh.NewMultiSelect[string]().
		Title("Features to keep").
		Options(options...).
		// FIXME: huh v2.0.3 subtracts the title height from the multiselect viewport
		// instead of the frame, so an unset Height scrolls one option at a time.
		// Drop this once the viewport sizing is fixed upstream.
		Height(len(options) + 1).
		Value(&chosen)

	err := huh.NewForm(huh.NewGroup(field)).
		WithTheme(theme).
		RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return withDependencies(catalogue, chosen), true, nil
}

func printCatalogue(stdout io.Writer, tier environmentv1.Tier, catalogue []*contractv1.Feature) {
	width := 0
	for _, f := range catalogue {
		width = max(width, len(f.GetName()))
	}

	fmt.Fprintf(stdout, "The %s bootstrap offers:\n\n", Name(tier))
	for _, f := range catalogue {
		fmt.Fprintf(stdout, "  %-*s   %s", width, f.GetName(), f.GetSummary())
		if deps := f.GetDependsOn(); len(deps) > 0 {
			fmt.Fprintf(stdout, "  (needs %s)", strings.Join(deps, ", "))
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintln(stdout)
}

func catalogueNames(catalogue []*contractv1.Feature) []string {
	names := make([]string, 0, len(catalogue))
	for _, f := range catalogue {
		names = append(names, f.GetName())
	}
	return names
}

func enabledFeatures(catalogue []*contractv1.Feature) []string {
	var enabled []string
	for _, f := range catalogue {
		if f.GetEnabled() {
			enabled = append(enabled, f.GetName())
		}
	}
	return enabled
}

func inCatalogueOrder(catalogue []*contractv1.Feature, chosen []string) []string {
	var ordered []string
	for _, f := range catalogue {
		if slices.Contains(chosen, f.GetName()) {
			ordered = append(ordered, f.GetName())
		}
	}
	return ordered
}

func withDependencies(catalogue []*contractv1.Feature, chosen []string) []string {
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

func parseFeatureFlag(raw string, catalogue []*contractv1.Feature) ([]string, error) {
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

func unmetDependency(catalogue []*contractv1.Feature, chosen []string) error {
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

func dependentProjects(catalogue []*contractv1.Feature, dropped []string) []string {
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
