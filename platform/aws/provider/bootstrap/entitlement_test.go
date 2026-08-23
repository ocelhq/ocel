package bootstrap

import (
	"context"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type freePlanEdge struct {
	*fakeEdge
	verifications int
}

func (f *freePlanEdge) VerifyCredentials(context.Context) (edge.CredentialIdentity, error) {
	f.verifications++
	return edge.CredentialIdentity{Account: "acct-1", Plan: "Workers Free", CodeEntitlement: edge.EntitlementWithheld}, nil
}

func TestRunNeverAsksWhatThePlanEntitles(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			ed := &freePlanEdge{fakeEdge: &fakeEdge{kind: "cloudflare"}}
			standInCloudflare(t, ed)

			if err := Run(context.Background(), apisOf(newFakeCFN(), newFakeSSM(), &fakeIAM{}, preloadedStore()), class, everything(), nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if ed.verifications != 0 {
				t.Errorf("bootstrap asked the edge to verify credentials %d times; a free plan bootstraps like any other", ed.verifications)
			}
		})
	}
}
