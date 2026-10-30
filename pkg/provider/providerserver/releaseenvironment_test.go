package providerserver_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
)

func TestTheRecordOfAReleaseNamesTheEnvironmentItWasReleasedFor(t *testing.T) {
	builtProject(t)
	for name, tc := range map[string]struct {
		tier    environmentv1.Tier
		request *contractv1.DeployRequest
		ledger  environment.Tier
		want    string
	}{
		"production": {environmentv1.Tier_TIER_PRODUCTION, deployRequest(), environment.TierProduction, "prod"},
		"a preview":  {environmentv1.Tier_TIER_PREVIEW, previewRequest(), environment.TierPreview, "pr-7"},
	} {
		t.Run(name, func(t *testing.T) {
			vendor := fake.NewProvider(fake.Options{})
			client := servedBy(t, vendor)
			bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: tc.tier})
			result, _ := deploy(t, client, tc.request)
			if !result.GetSuccess() {
				t.Fatalf("Deploy() = %q", result.GetError())
			}
			record, found, err := ledger.New(vendor.KeyValues(), tc.ledger, "shop").Record(context.Background(), "web", result.GetApps()[0].GetRelease())
			if err != nil || !found {
				t.Fatalf("Record(web) = (found %v, %v), want the record the deploy staged", found, err)
			}
			if record.Environment != tc.want {
				t.Errorf("the record of web's release names environment %q, want %q", record.Environment, tc.want)
			}
		})
	}
}
