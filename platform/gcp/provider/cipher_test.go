package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func sealingWithNoKeys(t *testing.T) (*Provider, *googletest.IAM) {
	t.Helper()
	server := &googletest.IAM{}
	return pushing(t, server.Open(t).Endpoint), server
}

func TestTheFirstValueATierSealsAddsTheKeysPartRecordedAsAddedByItsProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tier := environment.TierPreview
	p, server := sealingWithNoKeys(t)

	if _, err := p.Cipher().Seal(ctx, tier, seal.BindToProject("shop", "variable", "STRIPE_KEY"), []byte("sk_live")); err != nil {
		t.Fatalf("Seal() on a tier with no keys = %v, want the keys part added and the value sealed", err)
	}

	if !server.HasKeyRing(gcloud.KeyRingPath(p.resolved)) || !server.HasKey(gcloud.KeyPath(p.resolved, string(tier))) {
		t.Error("the first seal left no key ring or no tier key")
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, bootstrap.KeysPart)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("the keys part is recorded %+v, %t, %v, want it ready and added by shop", recorded, present, err)
	}
}

func TestTheFirstValueOfNoProjectATierSealsAddsTheTierKeyRecordedAsAddedByNoProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tier := environment.TierPreview
	p, server := sealingWithNoKeys(t)

	if _, err := p.Cipher().Seal(ctx, tier, seal.BindToProject("*", "variable", "DIGEST"), []byte("k")); err != nil {
		t.Fatalf("Seal() on a tier with no keys = %v", err)
	}

	if !server.HasKey(gcloud.KeyPath(p.resolved, string(tier))) {
		t.Errorf("the first seal of a value of no project left no %s key to seal it under", tier)
	}
	if recorded, _, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, bootstrap.KeysPart); err != nil || recorded.AddedBy != "" {
		t.Errorf("the keys part is recorded %+v, %v, want it added by no project: no slug names *", recorded, err)
	}
}
