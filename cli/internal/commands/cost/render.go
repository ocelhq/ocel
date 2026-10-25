package cost

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"math/big"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/terminal"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

const blockIndent = "  "

type pricedSet struct {
	priced   map[string]*costv1.ResourceEstimate
	byScope  map[string][]*costv1.Resource
	children map[string][]*costv1.Scope
	totals   map[string]string
}

func render(stdout io.Writer, palette terminal.Palette, slug string, set *costv1.ResourceSet, estimates map[costv1.Profile]*costv1.Estimate, profile costv1.Profile, assumptions []string) {
	chosen := estimates[profile]
	fmt.Fprintf(stdout, "%s\n\n", palette.Heading(slug, profileName(profile)+" profile", chosen.GetCurrency()+" per month", "rates "+chosen.GetRatesVersion()))

	resources := pricedSet{
		priced:   make(map[string]*costv1.ResourceEstimate, len(chosen.GetResources())),
		byScope:  make(map[string][]*costv1.Resource, len(set.GetScopes())),
		children: make(map[string][]*costv1.Scope, len(set.GetScopes())),
		totals:   make(map[string]string, len(chosen.GetScopes())),
	}
	for _, total := range chosen.GetScopes() {
		resources.totals[total.GetScope()] = sum(total.GetMonthlyFixed(), total.GetMonthlyUsage())
	}
	for _, resource := range chosen.GetResources() {
		resources.priced[resource.GetResource()] = resource
	}
	for _, resource := range set.GetResources() {
		resources.byScope[resource.GetScope()] = append(resources.byScope[resource.GetScope()], resource)
	}
	for _, scope := range set.GetScopes() {
		resources.children[scope.GetParent()] = append(resources.children[scope.GetParent()], scope)
	}

	table := terminal.NewTable(
		terminal.Column{Head: "RESOURCE"},
		terminal.Column{Head: "TYPE"},
		terminal.Column{Head: "FIXED", Align: terminal.AlignRight},
		terminal.Column{Head: "USAGE", Align: terminal.AlignRight},
		terminal.Column{Head: "MONTHLY", Align: terminal.AlignRight},
	)
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, scope := range resources.children[parent] {
			table.Row(indent(depth)+scopeLabel(palette, scope), "", "", "", palette.Muted(cmp.Or(resources.totals[scope.GetId()], "0.00")))
			for _, resource := range resources.byScope[scope.GetId()] {
				writeRow(table, palette, indent(depth+1), resource, resources.priced[resource.GetId()])
			}
			walk(scope.GetId(), depth+1)
		}
	}
	walk("", 0)
	table.Rule()
	table.Row(palette.Bold("Total"), "", formatCost(chosen.GetMonthlyFixed()), formatCost(chosen.GetMonthlyUsage()), palette.Bold(sum(chosen.GetMonthlyFixed(), chosen.GetMonthlyUsage())))
	fmt.Fprint(stdout, table.Render(palette, blockIndent))

	byProfile := make([]string, 0, len(estimates))
	for _, each := range profiles() {
		total := profileName(each) + " " + sum(estimates[each].GetMonthlyFixed(), estimates[each].GetMonthlyUsage())
		if each == profile {
			total = palette.Bold(total)
		}
		byProfile = append(byProfile, total)
	}
	fields := terminal.NewTable(terminal.Column{}, terminal.Column{})
	fields.Row(palette.Muted("By profile"), strings.Join(byProfile, palette.Muted(" · ")))
	fields.Row(palette.Muted("Coverage"), formatCoverage(chosen.GetCoverage()))
	fmt.Fprint(stdout, "\n"+fields.Render(palette, blockIndent))

	if len(assumptions)+len(chosen.GetNotes()) > 0 {
		fmt.Fprintln(stdout)
	}
	for _, assumption := range assumptions {
		fmt.Fprintf(stdout, "%s%s %s\n", blockIndent, palette.WarnMark(), palette.Commands(assumption))
	}
	for _, note := range chosen.GetNotes() {
		fmt.Fprintf(stdout, "%s%s %s\n", blockIndent, palette.NeutralMark(), palette.Commands(note))
	}
}

func formatCoverage(coverage *costv1.Coverage) string {
	unsupported := fmt.Sprintf("%d unsupported", coverage.GetUnsupported())
	if types := coverage.GetUnsupportedTypes(); len(types) > 0 {
		counted := make([]string, 0, len(types))
		for _, typ := range slices.Sorted(maps.Keys(types)) {
			counted = append(counted, fmt.Sprintf("%s ×%d", typ, types[typ]))
		}
		unsupported += " (" + strings.Join(counted, ", ") + ")"
	}
	return strings.Join([]string{
		fmt.Sprintf("%d priced", coverage.GetSupported()),
		fmt.Sprintf("%d free", coverage.GetFree()),
		unsupported,
		fmt.Sprintf("%d without a price", coverage.GetNoPrice()),
	}, " · ")
}

func writeRow(table *terminal.Table, palette terminal.Palette, indent string, resource *costv1.Resource, estimate *costv1.ResourceEstimate) {
	name := resource.GetName()
	if name == "" {
		name = resource.GetId()
	}
	name = indent + name
	switch estimate.GetStatus() {
	case costv1.ResourceEstimate_STATUS_PRICED:
		table.Row(name, resource.GetType(), formatCost(estimate.GetMonthlyFixed()), formatCost(estimate.GetMonthlyUsage()), sum(estimate.GetMonthlyFixed(), estimate.GetMonthlyUsage()))
	case costv1.ResourceEstimate_STATUS_FREE:
		table.Row(name, resource.GetType(), "", "", palette.Muted("free"))
	case costv1.ResourceEstimate_STATUS_NO_PRICE:
		table.Row(name, resource.GetType(), "", "", palette.Warning("no price"))
	default:
		table.Row(name, resource.GetType(), "", "", palette.Warning("unsupported"))
	}
}

func scopeLabel(palette terminal.Palette, scope *costv1.Scope) string {
	if scope.GetName() == "" {
		return palette.Bold(scope.GetKind())
	}
	return palette.Muted(scope.GetKind()) + " " + palette.Bold(scope.GetName())
}

func indent(depth int) string {
	return strings.Repeat("  ", depth)
}

func formatCost(amount string) string {
	if parseAmount(amount).Sign() == 0 {
		return terminal.NoValue
	}
	return amount
}

func parseAmount(amount string) *big.Rat {
	if parsed, ok := new(big.Rat).SetString(amount); ok {
		return parsed
	}
	return new(big.Rat)
}

func sum(amounts ...string) string {
	total := new(big.Rat)
	for _, amount := range amounts {
		total.Add(total, parseAmount(amount))
	}
	return total.FloatString(2)
}
