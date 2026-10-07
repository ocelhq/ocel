package providerserver

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
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
