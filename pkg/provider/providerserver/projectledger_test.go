package providerserver

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
)

var stagedSecrets = router.ReleaseSecrets{
	PurgeSecret: "the secret the edge verifies purges with",
	Env:         map[string]string{"PUBLIC_MODE": "loud"},
	Envelope:    "the data key the edge opens its secrets with",
}

func TestAStagedRecordsSecretsAreSealedInTheLedgerAndHandedToARouterOpen(t *testing.T) {
	ctx := context.Background()
	p := fake.NewProvider(fake.Options{})
	l := openProjectLedger(p, environment.TierProduction, "shop")

	if err := l.putStaged(ctx, router.ReleaseRecord{App: "web", Release: "b1", Secrets: stagedSecrets}); err != nil {
		t.Fatalf("putStaged: %v", err)
	}

	kept, _, err := l.Record(ctx, "web", "b1")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	stored, err := json.Marshal(kept)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Sealed) == 0 || !kept.Secrets.IsZero() {
		t.Errorf("the ledger keeps sealed %d bytes and secrets %+v, want the secrets sealed and none in the clear", len(kept.Sealed), kept.Secrets)
	}
	for _, plain := range []string{stagedSecrets.PurgeSecret, stagedSecrets.Envelope, "loud"} {
		if strings.Contains(string(stored), plain) || strings.Contains(string(kept.Sealed), plain) {
			t.Errorf("the ledger keeps %q in the clear, want every secret of the release sealed: whoever reads the origin's table would otherwise hold it", plain)
		}
	}
	records, err := l.readRecords(ctx, router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}, []string{"web"})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	got := records["web"].Secrets
	if got.PurgeSecret != stagedSecrets.PurgeSecret || got.Envelope != stagedSecrets.Envelope || !maps.Equal(got.Env, stagedSecrets.Env) {
		t.Errorf("a router is handed secrets %+v, want %+v, the ones the deploy staged", got, stagedSecrets)
	}
}

func TestTheLedgerRefusesARecordWhoseSecretsAreNotSealed(t *testing.T) {
	ctx := context.Background()
	l := openProjectLedger(fake.NewProvider(fake.Options{}), environment.TierProduction, "shop")

	if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "b1", Secrets: stagedSecrets}); err == nil {
		t.Error("PutStaged kept a record with plain secrets, want it refused: a release's secrets reach the ledger only sealed")
	}
}

func TestSecretsSealedForAnotherReleaseDoNotOpen(t *testing.T) {
	ctx := context.Background()
	p := fake.NewProvider(fake.Options{})
	l := openProjectLedger(p, environment.TierProduction, "shop")
	plain, err := json.Marshal(stagedSecrets)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := p.Cipher().Seal(ctx, environment.TierProduction, seal.BindToProject("shop", "release secrets", "web", "b1"), plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "b2", Sealed: sealed}); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	if _, err := l.readRecords(ctx, router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b2"}}, []string{"web"}); err == nil {
		t.Error("readRecords opened under release b2 secrets sealed for b1, want them refused: the secrets are bound to the release they serve")
	}
}
