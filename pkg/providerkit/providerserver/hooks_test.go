package providerserver_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func TestAHookGroupLeftNilIsSkippedAndOneSetRunsBothItsSteps(t *testing.T) {
	var shaped, estimated int
	counted := &provider.CostHooks{
		Shape: func(context.Context, provider.ShapeRequest) (*costv1.ResourceSet, error) {
			shaped++
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
		{"a provider that shapes and prices", counted, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shaped, estimated = 0, 0
			p := fake.NewProvider(fake.Options{Region: "nowhere"}).WithHooks(func(h *provider.Hooks) { h.Cost = tc.cost })
			client, rates := costServed(t, p)

			_, shapeErr := client.Shape(context.Background(), shapeRequest())
			_, priceErr := rates.Price(context.Background(), &costv1.PriceRequest{Resources: &costv1.ResourceSet{Source: "ocel"}})
			for call, err := range map[string]error{"Shape": shapeErr, "Price": priceErr} {
				if skipped := connect.CodeOf(err) == connect.CodeUnimplemented; skipped != tc.skip {
					t.Errorf("%s() error = %v, want skipped = %v", call, err, tc.skip)
				}
			}
			if shaped != tc.runs || estimated != tc.runs {
				t.Errorf("the group's steps ran shape %d, estimate %d times, want %d each: a group is present or absent as a whole", shaped, estimated, tc.runs)
			}
		})
	}
}
