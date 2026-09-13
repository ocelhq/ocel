package cost

import (
	"fmt"
	"io"
	"maps"
	"math/big"
	"slices"
	"strings"
	"text/tabwriter"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func render(stdout io.Writer, slug string, set *costv1.ResourceSet, estimates map[costv1.Profile]*costv1.Estimate, profile costv1.Profile, assumptions []string) error {
	chosen := estimates[profile]
	fmt.Fprintf(stdout, "%s · %s profile · %s per month · rates %s\n\n", slug, profileName(profile), chosen.GetCurrency(), chosen.GetRatesVersion())

	priced := make(map[string]*costv1.ResourceEstimate, len(chosen.GetResources()))
	for _, held := range chosen.GetResources() {
		priced[held.GetResource()] = held
	}
	byScope := make(map[string][]*costv1.Resource, len(set.GetScopes()))
	for _, resource := range set.GetResources() {
		byScope[resource.GetScope()] = append(byScope[resource.GetScope()], resource)
	}
	children := make(map[string][]*costv1.Scope, len(set.GetScopes()))
	for _, scope := range set.GetScopes() {
		children[scope.GetParent()] = append(children[scope.GetParent()], scope)
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RESOURCE\tTYPE\tFIXED\tUSAGE")
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, scope := range children[parent] {
			fmt.Fprintf(tw, "%s%s\n", indent(depth), scopeLabel(scope))
			for _, resource := range byScope[scope.GetId()] {
				writeRow(tw, indent(depth+1), resource, priced[resource.GetId()])
			}
			walk(scope.GetId(), depth+1)
		}
	}
	walk("", 0)
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Fixed %s + usage %s = %s per month on the %s profile\n", money(chosen.GetMonthlyFixed()), money(chosen.GetMonthlyUsage()), sum(chosen.GetMonthlyFixed(), chosen.GetMonthlyUsage()), profileName(profile))
	byProfile := make([]string, 0, len(estimates))
	for _, held := range profiles() {
		byProfile = append(byProfile, fmt.Sprintf("%s %s", profileName(held), money(estimates[held].GetMonthlyUsage())))
	}
	fmt.Fprintf(stdout, "Usage by profile: %s\n", strings.Join(byProfile, " · "))
	coverage := chosen.GetCoverage()
	fmt.Fprintf(stdout, "Coverage: %d priced, %d free, %d unsupported, %d without a price\n", coverage.GetSupported(), coverage.GetFree(), coverage.GetUnsupported(), coverage.GetNoPrice())
	for _, typ := range slices.Sorted(maps.Keys(coverage.GetUnsupportedTypes())) {
		fmt.Fprintf(stdout, "  unsupported: %s ×%d\n", typ, coverage.GetUnsupportedTypes()[typ])
	}
	for _, assumption := range assumptions {
		fmt.Fprintf(stdout, "Assumption: %s\n", assumption)
	}
	for _, note := range chosen.GetNotes() {
		fmt.Fprintf(stdout, "Note: %s\n", note)
	}
	return nil
}

func writeRow(tw io.Writer, indent string, resource *costv1.Resource, estimate *costv1.ResourceEstimate) {
	name := resource.GetName()
	if name == "" {
		name = resource.GetId()
	}
	switch estimate.GetStatus() {
	case costv1.ResourceEstimate_STATUS_PRICED:
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\n", indent, name, resource.GetType(), money(estimate.GetMonthlyFixed()), money(estimate.GetMonthlyUsage()))
	case costv1.ResourceEstimate_STATUS_FREE:
		fmt.Fprintf(tw, "%s%s\t%s\tfree\n", indent, name, resource.GetType())
	case costv1.ResourceEstimate_STATUS_NO_PRICE:
		fmt.Fprintf(tw, "%s%s\t%s\tno price\n", indent, name, resource.GetType())
	default:
		fmt.Fprintf(tw, "%s%s\t%s\tunsupported\n", indent, name, resource.GetType())
	}
}

func scopeLabel(scope *costv1.Scope) string {
	if scope.GetName() == "" {
		return scope.GetKind()
	}
	return scope.GetKind() + " " + scope.GetName()
}

func indent(depth int) string {
	return strings.Repeat("  ", depth)
}

func money(amount string) string {
	if amount == "" {
		return "0.00"
	}
	return amount
}

func sum(amounts ...string) string {
	total := new(big.Rat)
	for _, amount := range amounts {
		if amount == "" {
			continue
		}
		if held, ok := new(big.Rat).SetString(amount); ok {
			total.Add(total, held)
		}
	}
	return total.FloatString(2)
}
