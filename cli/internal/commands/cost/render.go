package cost

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func render(stdout io.Writer, label string, set *costv1.ResourceSet, estimates byProfile, profile costv1.Profile, assumptions []string) error {
	chosen := estimates[profile]
	fmt.Fprintf(stdout, "%s · %s profile · %s per month\n\n", label, pricing.ProfileName(profile), chosen.GetCurrency())

	priced := estimatesByResource(chosen)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RESOURCE\tTYPE\tFIXED\tUSAGE")
	walkScopes(set, func(scope *costv1.Scope, depth int) {
		fmt.Fprintf(tw, "%s%s\n", indent(depth), scopeLabel(scope))
	}, func(resource *costv1.Resource, depth int) {
		writeRow(tw, indent(depth), resource, priced[resource.GetId()])
		writeUnpriced(tw, indent(depth+1), priced[resource.GetId()])
	})
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Fixed %s + usage %s = %s per month on the %s profile\n", chosen.GetMonthlyFixed(), chosen.GetMonthlyUsage(), sum(chosen.GetMonthlyFixed(), chosen.GetMonthlyUsage()), pricing.ProfileName(profile))
	byProfile := make([]string, 0, len(estimates))
	for _, each := range pricing.Profiles() {
		byProfile = append(byProfile, fmt.Sprintf("%s %s", pricing.ProfileName(each), estimates[each].GetMonthlyUsage()))
	}
	fmt.Fprintf(stdout, "Usage by profile: %s\n", strings.Join(byProfile, " · "))
	coverage := chosen.GetCoverage()
	fmt.Fprintf(stdout, "Coverage: %d priced, %d free, %d unsupported, %d without a price\n", coverage.GetSupported(), coverage.GetFree(), coverage.GetUnsupported(), coverage.GetNoPrice())
	for _, typ := range slices.Sorted(maps.Keys(coverage.GetUnsupportedTypes())) {
		fmt.Fprintf(stdout, "  unsupported: %s ×%d\n", typ, coverage.GetUnsupportedTypes()[typ])
	}
	if n := countUnpriced(chosen); n > 0 {
		fmt.Fprintf(stdout, "Unpriced: %s no price, so these totals leave %s out\n", componentsHave(n), them(n))
	}
	writeFooter(stdout, chosen.GetCatalogs(), assumptions, chosen.GetNotes())
	return nil
}

func renderDiff(stdout io.Writer, label string, currentSet, proposedSet *costv1.ResourceSet, diffs map[costv1.Profile]*costv1.EstimateDiff, profile costv1.Profile, assumptions []string) error {
	chosen := diffs[profile]
	current, proposed := chosen.GetCurrent(), chosen.GetProposed()
	fmt.Fprintf(stdout, "%s · %s profile · %s per month\n\n", label, pricing.ProfileName(profile), proposed.GetCurrency())

	if len(chosen.GetResources()) == 0 {
		fmt.Fprintln(stdout, "No resource changes its monthly cost.")
	} else {
		types := resourceTypes(currentSet, proposedSet)
		before, after := estimatesByResource(current), estimatesByResource(proposed)
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "RESOURCE\tTYPE\tCHANGE\tFIXED\tUSAGE")
		for _, change := range chosen.GetResources() {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", change.GetResource(), types[change.GetResource()], changeLabel(change.GetChange()), changed(change.GetMonthlyFixedDelta()), changed(change.GetMonthlyUsageDelta()))
			side := after[change.GetResource()]
			if change.GetChange() == costv1.ResourceChange_CHANGE_REMOVED {
				side = before[change.GetResource()]
			}
			writeUnpriced(tw, indent(1), side)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Fixed %s + usage %s = %s per month on the %s profile\n", signed(chosen.GetMonthlyFixedDelta()), signed(chosen.GetMonthlyUsageDelta()), signed(sum(chosen.GetMonthlyFixedDelta(), chosen.GetMonthlyUsageDelta())), pricing.ProfileName(profile))
	fmt.Fprintf(stdout, "Current %s → proposed %s per month\n", sum(current.GetMonthlyFixed(), current.GetMonthlyUsage()), sum(proposed.GetMonthlyFixed(), proposed.GetMonthlyUsage()))
	byProfile := make([]string, 0, len(diffs))
	for _, each := range pricing.Profiles() {
		byProfile = append(byProfile, fmt.Sprintf("%s %s", pricing.ProfileName(each), signed(diffs[each].GetMonthlyUsageDelta())))
	}
	fmt.Fprintf(stdout, "Usage change by profile: %s\n", strings.Join(byProfile, " · "))
	if n, m := countUnpriced(proposed), countUnpriced(current); n+m > 0 {
		fmt.Fprintf(stdout, "Unpriced: %s in the proposed set and %s in the current set have no price, so these totals leave them out\n", components(n), components(m))
	}
	writeFooter(stdout, mergeCatalogs(current.GetCatalogs(), proposed.GetCatalogs()), assumptions, mergeNotes(current.GetNotes(), proposed.GetNotes()))
	return nil
}

func writeFooter(stdout io.Writer, catalogs []*costv1.CatalogVersion, assumptions, notes []string) {
	if len(catalogs) > 0 {
		fmt.Fprintf(stdout, "Prices: %s\n", catalogLabel(catalogs))
	}
	for _, assumption := range assumptions {
		fmt.Fprintf(stdout, "Assumption: %s\n", assumption)
	}
	for _, note := range notes {
		fmt.Fprintf(stdout, "Note: %s\n", note)
	}
}

func walkScopes(set *costv1.ResourceSet, onScope func(*costv1.Scope, int), onResource func(*costv1.Resource, int)) {
	known := make(map[string]bool, len(set.GetScopes()))
	children := make(map[string][]*costv1.Scope, len(set.GetScopes()))
	for _, scope := range set.GetScopes() {
		known[scope.GetId()] = true
	}
	for _, scope := range set.GetScopes() {
		parent := scope.GetParent()
		if !known[parent] {
			parent = ""
		}
		children[parent] = append(children[parent], scope)
	}
	byScope := make(map[string][]*costv1.Resource, len(set.GetScopes()))
	for _, resource := range set.GetResources() {
		scope := resource.GetScope()
		if !known[scope] {
			scope = ""
		}
		byScope[scope] = append(byScope[scope], resource)
	}
	shown := map[string]bool{"": true}
	var show func(scope *costv1.Scope, depth int)
	show = func(scope *costv1.Scope, depth int) {
		if shown[scope.GetId()] {
			return
		}
		shown[scope.GetId()] = true
		onScope(scope, depth)
		for _, resource := range byScope[scope.GetId()] {
			onResource(resource, depth+1)
		}
		for _, child := range children[scope.GetId()] {
			show(child, depth+1)
		}
	}
	for _, resource := range byScope[""] {
		onResource(resource, 0)
	}
	for _, scope := range children[""] {
		show(scope, 0)
	}
	for _, scope := range set.GetScopes() {
		show(scope, 0)
	}
}

func estimatesByResource(estimate *costv1.Estimate) map[string]*costv1.ResourceEstimate {
	byID := make(map[string]*costv1.ResourceEstimate, len(estimate.GetResources()))
	for _, resource := range estimate.GetResources() {
		byID[resource.GetResource()] = resource
	}
	return byID
}

func writeRow(tw io.Writer, indent string, resource *costv1.Resource, estimate *costv1.ResourceEstimate) {
	name := resource.GetName()
	if name == "" {
		name = resource.GetId()
	}
	switch estimate.GetStatus() {
	case costv1.ResourceEstimate_STATUS_PRICED:
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\n", indent, name, resource.GetType(), estimate.GetMonthlyFixed(), estimate.GetMonthlyUsage())
	case costv1.ResourceEstimate_STATUS_FREE:
		fmt.Fprintf(tw, "%s%s\t%s\tfree\n", indent, name, resource.GetType())
	case costv1.ResourceEstimate_STATUS_NO_PRICE:
		fmt.Fprintf(tw, "%s%s\t%s\tno price\n", indent, name, resource.GetType())
	default:
		fmt.Fprintf(tw, "%s%s\t%s\tunsupported\n", indent, name, resource.GetType())
	}
}

func writeUnpriced(tw io.Writer, indent string, estimate *costv1.ResourceEstimate) {
	for _, component := range estimate.GetComponents() {
		if pricing.Unpriced(component) {
			fmt.Fprintf(tw, "%s! %s: %s\n", indent, component.GetName(), unpricedReason(component))
		}
	}
}

func unpricedReason(component *costv1.CostComponent) string {
	detail := component.GetPriceDetail()
	switch component.GetPriceStatus() {
	case costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT:
		return "unknown until applied: " + strings.Join(component.GetDependsOnUnknown(), ", ")
	case costv1.CostComponent_PRICE_STATUS_AMBIGUOUS:
		return withDetail("ambiguous price", detail)
	case costv1.CostComponent_PRICE_STATUS_UNAVAILABLE:
		return withDetail("no cached price", detail)
	}
	return withDetail("no price", detail)
}

func withDetail(label, detail string) string {
	if detail == "" {
		return label
	}
	return label + ", " + detail
}

func countUnpriced(estimate *costv1.Estimate) int {
	n := 0
	for _, resource := range estimate.GetResources() {
		for _, component := range resource.GetComponents() {
			if pricing.Unpriced(component) {
				n++
			}
		}
	}
	return n
}

func components(n int) string {
	if n == 1 {
		return "1 component"
	}
	return fmt.Sprintf("%d components", n)
}

func componentsHave(n int) string {
	if n == 1 {
		return "1 component has"
	}
	return fmt.Sprintf("%d components have", n)
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func resourceTypes(sets ...*costv1.ResourceSet) map[string]string {
	types := map[string]string{}
	for _, set := range sets {
		for _, resource := range set.GetResources() {
			types[resource.GetId()] = resource.GetType()
		}
	}
	return types
}

func changeLabel(change costv1.ResourceChange_Change) string {
	switch change {
	case costv1.ResourceChange_CHANGE_ADDED:
		return "added"
	case costv1.ResourceChange_CHANGE_REMOVED:
		return "removed"
	}
	return "changed"
}

func mergeCatalogs(lists ...[]*costv1.CatalogVersion) []*costv1.CatalogVersion {
	seen := map[string]bool{}
	var merged []*costv1.CatalogVersion
	for _, list := range lists {
		for _, version := range list {
			key := version.GetVendor() + "\x00" + version.GetService() + "\x00" + version.GetVersion()
			if !seen[key] {
				seen[key] = true
				merged = append(merged, version)
			}
		}
	}
	slices.SortFunc(merged, func(a, b *costv1.CatalogVersion) int {
		return strings.Compare(a.GetVendor()+"\x00"+a.GetService()+"\x00"+a.GetVersion(), b.GetVendor()+"\x00"+b.GetService()+"\x00"+b.GetVersion())
	})
	return merged
}

func mergeNotes(lists ...[]string) []string {
	var merged []string
	for _, list := range lists {
		for _, note := range list {
			if !slices.Contains(merged, note) {
				merged = append(merged, note)
			}
		}
	}
	return merged
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
