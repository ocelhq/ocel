package bootstrap

import (
	"context"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type freePlanEdge struct {
	*fakeEdge
	checks int
}

func (f *freePlanEdge) CodeEntitlement(context.Context) (edge.CodeEntitlement, error) {
	f.checks++
	return edge.CodeEntitlement{Plan: "Workers Free", Granted: edge.EntitlementWithheld}, nil
}

func TestRunNeverAsksWhatThePlanEntitles(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			ed := &freePlanEdge{fakeEdge: &fakeEdge{kind: "cloudflare"}}
			frontedBy(t, ed)

			if err := Run(context.Background(), apisOf(newFakeCFN(), newFakeSSM(), &fakeIAM{}, preloadedStore()), class, everything(), nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if ed.checks != 0 {
				t.Errorf("bootstrap asked the edge what its plan entitles %d times; a free plan bootstraps like any other", ed.checks)
			}
		})
	}
}
