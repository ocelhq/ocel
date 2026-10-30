package providerserver_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAHookGroupLeftNilIsSkippedAndOneSetRunsBothItsSteps(t *testing.T) {
	var inventoried, estimated int
	counted := &provider.CostHooks{
		Inventory: func(context.Context, provider.InventoryRequest) (*costv1.ResourceSet, error) {
			inventoried++
			return &costv1.ResourceSet{}, nil
		},
		Estimate: func(context.Context, *costv1.PriceRequest) (*costv1.Estimate, error) {
			estimated++
			return &costv1.Estimate{}, nil
		},
	}
	for _, tc := range []struct {
		name string
		cost *provider.CostHooks
		skip bool
		runs int
	}{
		{"a provider that prices nothing", nil, true, 0},
		{"a provider that inventories and prices", counted, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inventoried, estimated = 0, 0
			p := fake.NewProvider(fake.Options{Region: "nowhere"}).WithHooks(func(h *provider.Hooks) { h.Cost = tc.cost })
			client, rates := costServed(t, p)

			_, inventoryErr := client.Inventory(context.Background(), inventoryRequest())
			_, priceErr := rates.Price(context.Background(), &costv1.PriceRequest{Resources: &costv1.ResourceSet{Source: "ocel"}})
			for call, err := range map[string]error{"Inventory": inventoryErr, "Price": priceErr} {
				if skipped := connect.CodeOf(err) == connect.CodeUnimplemented; skipped != tc.skip {
					t.Errorf("%s() error = %v, want skipped = %v", call, err, tc.skip)
				}
			}
			if inventoried != tc.runs || estimated != tc.runs {
				t.Errorf("the group's steps ran inventory %d, estimate %d times, want %d each: a group is present or absent as a whole", inventoried, estimated, tc.runs)
			}
		})
	}
}
