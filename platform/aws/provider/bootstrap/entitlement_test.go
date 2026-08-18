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
	for _, tc := range []struct {
		name string
		run  func(context.Context, CFNAPI, SSMAPI, IAMAPI, edge.Edge, Artifacts, func(string), func(string)) error
	}{
		{"production", Run},
		{"preview", RunPreview},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ed := &freePlanEdge{fakeEdge: &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}}

			if err := tc.run(context.Background(), newFakeCFN(), newFakeSSM(), &fakeIAM{}, ed, preloadedArtifact(), nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if ed.verifications != 0 {
				t.Errorf("bootstrap asked the edge to verify credentials %d times; a free plan bootstraps like any other", ed.verifications)
			}
		})
	}
}
