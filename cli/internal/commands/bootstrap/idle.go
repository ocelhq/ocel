package bootstrap

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

const idleCurrency = "USD"

type idleCosts map[string]string

func priceIdle(ctx context.Context, invocation commands.Invocation, read []tierStatus, offline bool) []idleCosts {
	idle := make([]idleCosts, len(read))
	catalog, err := invocation.OpenPriceCatalog(offline)
	if err != nil {
		return idle
	}
	var inputs []pricing.Input
	var priced []int
	for i, status := range read {
		inventory := status.described.GetInventory()
		if len(inventory.GetResources()) == 0 {
			continue
		}
		inputs = append(inputs, pricing.Input{Set: inventory, Usage: &costv1.Usage{Profile: pricing.DefaultProfile}})
		priced = append(priced, i)
	}
	estimates, err := pricing.EstimateAll(ctx, catalog, invocation.Prices.Table, inputs)
	if err != nil {
		return idle
	}
	byStatus := make([]*costv1.Estimate, len(read))
	for j, i := range priced {
		byStatus[i] = estimates[j]
	}
	for i, status := range read {
		idle[i] = partIdleCosts(status.described.GetInventory(), byStatus[i])
	}
	return idle
}

func partIdleCosts(inventory *costv1.ResourceSet, estimate *costv1.Estimate) idleCosts {
	costs := idleCosts{}
	if estimate == nil {
		return costs
	}
	totals := make(map[string]string, len(estimate.GetScopes()))
	for _, total := range estimate.GetScopes() {
		totals[total.GetScope()] = total.GetMonthlyFixed()
	}
	unpriced := map[string]bool{}
	byID := make(map[string]*costv1.ResourceEstimate, len(estimate.GetResources()))
	for _, resource := range estimate.GetResources() {
		byID[resource.GetResource()] = resource
	}
	parentOf := make(map[string]string, len(inventory.GetScopes()))
	for _, scope := range inventory.GetScopes() {
		parentOf[scope.GetId()] = scope.GetParent()
	}
	for _, resource := range inventory.GetResources() {
		if isFullyPriced(byID[resource.GetId()]) {
			continue
		}
		for scope := resource.GetScope(); scope != "" && !unpriced[scope]; scope = parentOf[scope] {
			unpriced[scope] = true
		}
	}
	for _, scope := range inventory.GetScopes() {
		if scope.GetKind() != pricing.ScopePart || unpriced[scope.GetId()] {
			continue
		}
		if total, ok := totals[scope.GetId()]; ok {
			costs[scope.GetName()] = total
		} else {
			costs[scope.GetName()] = "0.00"
		}
	}
	return costs
}

func isFullyPriced(estimate *costv1.ResourceEstimate) bool {
	switch estimate.GetStatus() {
	case costv1.ResourceEstimate_STATUS_FREE:
		return true
	case costv1.ResourceEstimate_STATUS_PRICED:
		return !slices.ContainsFunc(estimate.GetComponents(), pricing.Unpriced)
	}
	return false
}
