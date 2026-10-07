package providerserver

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
)

func TestAStagedEnvelopeIsSealedInTheLedgerAndHandedToARouterOpen(t *testing.T) {
	ctx := context.Background()
	l := openProjectLedger(fake.NewProvider(fake.Options{}), environment.TierProduction, "shop")
	const envelope = "the data key the edge opens its secrets with"

	if err := l.putStaged(ctx, router.ReleaseRecord{App: "web", Release: "b1", Envelope: envelope}); err != nil {
		t.Fatalf("putStaged: %v", err)
	}

	kept, _, err := l.Record(ctx, "web", "b1")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if kept.Envelope == "" || kept.Envelope == envelope {
		t.Errorf("the ledger keeps the envelope as %q, want it sealed: whoever reads the origin's table would otherwise hold the key to every secret the edge serves", kept.Envelope)
	}
	records, err := l.readRecords(ctx, router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}, []string{"web"})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if got := records["web"].Envelope; got != envelope {
		t.Errorf("a router is handed the envelope %q, want %q, the one the deploy staged", got, envelope)
	}
}

func TestAnEnvelopeSealedForAnotherReleaseDoesNotOpen(t *testing.T) {
	ctx := context.Background()
	p := fake.NewProvider(fake.Options{})
	l := openProjectLedger(p, environment.TierProduction, "shop")
	sealed, err := p.Cipher().Seal(ctx, environment.TierProduction, seal.AssociatedData{
		{Name: "project", Value: "shop"},
		{Name: "app", Value: "web"},
		{Name: "release", Value: "b1"},
		{Name: "field", Value: "envelope"},
	}, []byte("the data key the edge opens its secrets with"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "b2", Envelope: base64.StdEncoding.EncodeToString(sealed)}); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	if _, err := l.readRecords(ctx, router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b2"}}, []string{"web"}); err == nil {
		t.Error("readRecords opened under release b2 an envelope sealed for b1, want it refused: the envelope is bound to the release it serves")
	}
}
