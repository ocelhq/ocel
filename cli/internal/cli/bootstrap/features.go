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

	"github.com/ocelhq/ocel/cli/internal/cli/style"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	allFeatures = "all"
	noFeatures  = "none"

	needsEdgePrefix = "edge:"
)

func tint(stdout io.Writer, attrs ...color.Attribute) *color.Color {
	return gated(stdout, color.New(attrs...))
}

func gated(stdout io.Writer, c *color.Color) *color.Color {
	if runui.IsTerminal(stdout) && !color.NoColor {
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

func chooseFeatures(ctx context.Context, opts Options, catalogue []*contractv1.Feature, standing, going []string, kind string, tier environmentv1.Tier, interactive bool, stdout io.Writer) ([]string, bool, error) {
	if opts.FeaturesDeclared {
		requested, err := parseFeatureFlag(opts.Features, catalogue)
		return requested, err == nil, err
	}
	if !interactive {
		return without(standing, going), true, nil
	}
	return pickFeatures(ctx, catalogue, standing, going, kind, tier, stdout)
}

func pickFeatures(ctx context.Context, catalogue []*contractv1.Feature, standing, going []string, kind string, tier environmentv1.Tier, stdout io.Writer) ([]string, bool, error) {
	if len(catalogue) == 0 {
		return nil, true, nil
	}

	kept := without(standing, going)
	required := requiredFeature(catalogue, kept, kind)

	printIncluded(stdout, catalogue, kept, tier)
	printRequired(stdout, catalogue, required, kind)

	addable := addableFeatures(catalogue, standing, required)
	width := nameWidth(addable)
	options := make([]huh.Option[string], 0, len(addable))
	for _, name := range addable {
		options = append(options, huh.NewOption(featureRow(stdout, catalogue, name, width), name))
	}

	var chosen []string
	if len(options) == 0 {
		if required == "" {
			fmt.Fprintln(stdout, "Everything is already included.")
			return kept, true, nil
		}
	} else {
		field := huh.NewMultiSelect[string]().
			Title("Select features to add").
			Options(options...).
			// FIXME: huh v2.0.3 subtracts the title height from the multiselect viewport
			// instead of the frame, so an unset Height scrolls one option at a time.
			// Drop this once the viewport sizing is fixed upstream.
			Height(len(options) + 1).
			Value(&chosen)

		err := huh.NewForm(huh.NewGroup(field)).
			WithTheme(style.Theme).
			RunWithContext(ctx)
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
	}

	if required != "" {
		chosen = append(chosen, required)
	}
	picked := inCatalogueOrder(catalogue, chosen)
	applied := withDependencies(catalogue, inCatalogueOrder(catalogue, append(slices.Clone(kept), picked...)))
	printAdded(stdout, catalogue, applied, kept, picked)
	return applied, true, nil
}

func requiredFeature(catalogue []*contractv1.Feature, included []string, kind string) string {
	name := featureNeedingEdge(catalogue, kind)
	if slices.Contains(included, name) {
		return ""
	}
	return name
}

func addableFeatures(catalogue []*contractv1.Feature, standing []string, required string) []string {
	var addable []string
	for _, f := range catalogue {
		if f.GetName() == required || slices.Contains(standing, f.GetName()) {
			continue
		}
		addable = append(addable, f.GetName())
	}
	return addable
}

func nameWidth(names []string) int {
	width := 0
	for _, name := range names {
		width = max(width, len(name))
	}
	return width
}

func featureRow(stdout io.Writer, catalogue []*contractv1.Feature, name string, width int) string {
	f := catalogueEntry(catalogue, name)
	row := fmt.Sprintf("%-*s   %s", width, name, f.GetSummary())
	if deps := f.GetDependsOn(); len(deps) > 0 {
		row += "  " + needsNote(stdout, "(needs "+strings.Join(deps, ", ")+")")
	}
	return row
}

func catalogueEntry(catalogue []*contractv1.Feature, name string) *contractv1.Feature {
	for _, f := range catalogue {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func printIncluded(stdout io.Writer, catalogue []*contractv1.Feature, included []string, tier environmentv1.Tier) {
	printSection(stdout, catalogue, included,
		fmt.Sprintf("Included in the %s bootstrap:", Name(tier)),
		fmt.Sprintf("To take one down: ocel bootstrap %s --remove <name>", Name(tier)))
}

func printRequired(stdout io.Writer, catalogue []*contractv1.Feature, required, kind string) {
	if required == "" {
		return
	}
	printSection(stdout, catalogue, []string{required}, "Required by this project:",
		fmt.Sprintf("Your edge is %s. Change it in %s.", kind, projectconfig.ConfigFileName))
}

func printSection(stdout io.Writer, catalogue []*contractv1.Feature, names []string, heading, note string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(stdout, "%s\n\n", heading)
	width := nameWidth(names)
	for _, name := range names {
		fmt.Fprintf(stdout, "  %s %s\n", selectedMark(stdout), featureRow(stdout, catalogue, name, width))
	}
	fmt.Fprintf(stdout, "\n  %s\n\n", note)
}

func printAdded(stdout io.Writer, catalogue []*contractv1.Feature, applied, included, picked []string) {
	if len(picked) == 0 {
		fmt.Fprintln(stdout, "Nothing to add.")
		return
	}
	fmt.Fprintf(stdout, "%s Adding %s\n", selectedMark(stdout), strings.Join(picked, ", "))
	for _, name := range without(without(applied, included), picked) {
		fmt.Fprintf(stdout, "  + %s %s\n", name,
			needsNote(stdout, "— "+needsItPhrase(directDependents(catalogue, name, applied))))
	}
}

func needsItPhrase(dependents []string) string {
	if len(dependents) > 1 {
		return strings.Join(dependents, ", ") + " need it"
	}
	return strings.Join(dependents, ", ") + " needs it"
}

type implication struct {
	name   string
	reason string
}

func impliedFeatures(catalogue []*contractv1.Feature, requested []string, kind string) []implication {
	fronting := featureNeedingEdge(catalogue, kind)
	named := slices.Clone(requested)
	if fronting != "" && !slices.Contains(named, fronting) {
		named = append(named, fronting)
	}
	applied := withDependencies(catalogue, inCatalogueOrder(catalogue, named))

	var pulled []implication
	if fronting != "" && !slices.Contains(requested, fronting) {
		pulled = append(pulled, implication{name: fronting, reason: "this project's edge needs it"})
	}
	for _, name := range applied {
		if name == fronting || slices.Contains(requested, name) {
			continue
		}
		pulled = append(pulled, implication{
			name:   name,
			reason: needsItPhrase(directDependents(catalogue, name, applied)),
		})
	}
	return pulled
}

func featureNeedingEdge(catalogue []*contractv1.Feature, kind string) string {
	if kind == "" {
		return ""
	}
	for _, f := range catalogue {
		if slices.Contains(f.GetNeeds(), needsEdgePrefix+kind) {
			return f.GetName()
		}
	}
	return ""
}

func printImplied(stdout io.Writer, pulled []implication) {
	for _, p := range pulled {
		fmt.Fprintf(stdout, "Also adding: %s %s\n", p.name, needsNote(stdout, "— "+p.reason))
	}
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

func parseRemoveFlag(raw string, catalogue []*contractv1.Feature) ([]string, error) {
	var named []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(catalogueNames(catalogue), name) {
			return nil, fmt.Errorf("this provider has no bootstrap feature named %q; it offers %s",
				name, strings.Join(catalogueNames(catalogue), ", "))
		}
		if !slices.Contains(named, name) {
			named = append(named, name)
		}
	}
	return inCatalogueOrder(catalogue, named), nil
}

func goingFeatures(catalogue []*contractv1.Feature, standing, named []string) []string {
	var doomed []string
	for _, name := range named {
		if slices.Contains(standing, name) {
			doomed = append(doomed, name)
		}
	}
	for grew := true; grew; {
		grew = false
		for _, f := range catalogue {
			if slices.Contains(doomed, f.GetName()) || !slices.Contains(standing, f.GetName()) {
				continue
			}
			for _, dep := range f.GetDependsOn() {
				if slices.Contains(doomed, dep) {
					doomed, grew = append(doomed, f.GetName()), true
				}
			}
		}
	}
	return inCatalogueOrder(catalogue, doomed)
}

func without(names, taken []string) []string {
	var kept []string
	for _, name := range names {
		if !slices.Contains(taken, name) {
			kept = append(kept, name)
		}
	}
	return kept
}

func bothWays(requested, named []string) error {
	var both []string
	for _, name := range named {
		if slices.Contains(requested, name) {
			both = append(both, name)
		}
	}
	if len(both) == 0 {
		return nil
	}
	return fmt.Errorf("--features and --remove both name %s; a feature is either ensured or taken down, never both in one run",
		strings.Join(both, ", "))
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
