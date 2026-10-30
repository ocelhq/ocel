package pricing

import (
	"github.com/shopspring/decimal"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func Diff(current, proposed *costv1.ResourceSet, currentEstimate, proposedEstimate *costv1.Estimate) *costv1.EstimateDiff {
	before := estimatesByResource(currentEstimate)
	after := estimatesByResource(proposedEstimate)
	inCurrent := resourceIDs(current)
	inProposed := resourceIDs(proposed)

	diff := &costv1.EstimateDiff{Current: currentEstimate, Proposed: proposedEstimate}
	var totalFixed, totalUsage decimal.Decimal
	record := func(id string, change costv1.ResourceChange_Change, was, now *costv1.ResourceEstimate) {
		fixed, fixedKnown := sideDelta(was, now, false)
		usage, usageKnown := sideDelta(was, now, true)
		alike := known(was, false) == known(now, false) && known(was, true) == known(now, true)
		if change == costv1.ResourceChange_CHANGE_MODIFIED && alike && fixed.IsZero() && usage.IsZero() {
			return
		}
		out := &costv1.ResourceChange{Resource: id, Change: change}
		if fixedKnown {
			out.MonthlyFixedDelta = money(fixed)
			totalFixed = totalFixed.Add(fixed)
		}
		if usageKnown {
			out.MonthlyUsageDelta = money(usage)
			totalUsage = totalUsage.Add(usage)
		}
		diff.Resources = append(diff.Resources, out)
	}
	for _, resource := range proposed.GetResources() {
		id := resource.GetId()
		if inCurrent[id] {
			record(id, costv1.ResourceChange_CHANGE_MODIFIED, before[id], after[id])
		} else {
			record(id, costv1.ResourceChange_CHANGE_ADDED, nil, after[id])
		}
	}
	for _, resource := range current.GetResources() {
		if id := resource.GetId(); !inProposed[id] {
			record(id, costv1.ResourceChange_CHANGE_REMOVED, before[id], nil)
		}
	}
	diff.MonthlyFixedDelta = money(totalFixed)
	diff.MonthlyUsageDelta = money(totalUsage)
	return diff
}

func sideDelta(was, now *costv1.ResourceEstimate, usageBased bool) (decimal.Decimal, bool) {
	wasAmount, nowAmount := side(was, usageBased), side(now, usageBased)
	return nowAmount.Sub(wasAmount), known(was, usageBased) && known(now, usageBased)
}

func side(estimate *costv1.ResourceEstimate, usageBased bool) decimal.Decimal {
	if usageBased {
		return amount(estimate.GetMonthlyUsage())
	}
	return amount(estimate.GetMonthlyFixed())
}

func known(estimate *costv1.ResourceEstimate, usageBased bool) bool {
	if estimate == nil {
		return true
	}
	if estimate.GetStatus() == costv1.ResourceEstimate_STATUS_UNSUPPORTED {
		return false
	}
	for _, component := range estimate.GetComponents() {
		if component.GetUsageBased() == usageBased && Unpriced(component) {
			return false
		}
	}
	return true
}

func estimatesByResource(estimate *costv1.Estimate) map[string]*costv1.ResourceEstimate {
	byID := make(map[string]*costv1.ResourceEstimate, len(estimate.GetResources()))
	for _, resource := range estimate.GetResources() {
		byID[resource.GetResource()] = resource
	}
	return byID
}

func resourceIDs(set *costv1.ResourceSet) map[string]bool {
	ids := make(map[string]bool, len(set.GetResources()))
	for _, resource := range set.GetResources() {
		ids[resource.GetId()] = true
	}
	return ids
}

func amount(text string) decimal.Decimal {
	parsed, err := decimal.NewFromString(text)
	if err != nil {
		return decimal.Zero
	}
	return parsed
}
