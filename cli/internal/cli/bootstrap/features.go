package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"github.com/fatih/color"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	allFeatures = "all"
	noFeatures  = "none"
)

func tint(stdout io.Writer, attrs ...color.Attribute) *color.Color {
	return gated(stdout, color.New(attrs...))
}

func gated(stdout io.Writer, c *color.Color) *color.Color {
	if deployui.IsTerminal(stdout) && !color.NoColor {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}

func selectedMark(stdout io.Writer) string {
	return tint(stdout, color.FgGreen).Sprint("✓")
}

func needsNote(stdout io.Writer, note string) string {
	return gated(stdout, color.RGB(0xff, 0xb8, 0x6c)).Sprint(note)
}

func chooseFeatures(ctx context.Context, opts Options, catalogue []*contractv1.Feature, interactive bool, stdout io.Writer) ([]string, bool, error) {
	if opts.FeaturesDeclared {
		requested, err := parseFeatureFlag(opts.Features, catalogue)
		return requested, err == nil, err
	}
	if !interactive {
		return enabledFeatures(catalogue), true, nil
	}
	return pickFeatures(ctx, catalogue, stdout)
}

func pickFeatures(ctx context.Context, catalogue []*contractv1.Feature, stdout io.Writer) ([]string, bool, error) {
	if len(catalogue) == 0 {
		return nil, true, nil
	}

	printCatalogue(stdout, catalogue)

	enabled := enabledFeatures(catalogue)
	options := make([]huh.Option[string], 0, len(catalogue))
	for _, f := range catalogue {
		options = append(options, huh.NewOption(f.GetName(), f.GetName()).Selected(slices.Contains(enabled, f.GetName())))
	}

	chosen := slices.Clone(enabled)
	field := huh.NewMultiSelect[string]().
		Title("Select all that apply").
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
	applied := withDependencies(catalogue, chosen)
	printApplied(stdout, catalogue, applied, chosen)
	return applied, true, nil
}

func printApplied(stdout io.Writer, catalogue []*contractv1.Feature, applied, chosen []string) {
	if len(applied) == 0 {
		fmt.Fprintln(stdout, "No features will be applied.")
		return
	}
	parts := make([]string, 0, len(applied))
	for _, name := range applied {
		if slices.Contains(chosen, name) {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, name+" "+needsNote(stdout, "(needed by "+strings.Join(directDependents(catalogue, name, applied), ", ")+")"))
	}
	fmt.Fprintf(stdout, "%s Selected %s\n", selectedMark(stdout), strings.Join(parts, ", "))
}

func printCatalogue(stdout io.Writer, catalogue []*contractv1.Feature) {
	width := 0
	for _, f := range catalogue {
		width = max(width, len(f.GetName()))
	}

	fmt.Fprint(stdout, "This provider has optional features:\n\n")
	for _, f := range catalogue {
		fmt.Fprintf(stdout, "  %-*s   %s", width, f.GetName(), f.GetSummary())
		if deps := f.GetDependsOn(); len(deps) > 0 {
			fmt.Fprintf(stdout, "  %s", needsNote(stdout, "(needs "+strings.Join(deps, ", ")+")"))
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintln(stdout)
}

func directDependents(catalogue []*contractv1.Feature, name string, within []string) []string {
	var dependents []string
	for _, f := range catalogue {
		if f.GetName() == name || !slices.Contains(within, f.GetName()) {
			continue
		}
		if slices.Contains(f.GetDependsOn(), name) {
			dependents = append(dependents, f.GetName())
		}
	}
	return dependents
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
