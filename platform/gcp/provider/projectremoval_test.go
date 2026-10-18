package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestRemovingAProjectBehindTheALBPlansReleasingItInOneGroupForTheProject(t *testing.T) {
	t.Parallel()
	b := bootstrap.Engine{Clients: googletest.Clients("http://127.0.0.1:1"), Records: fake.NewKeyValues()}
	tier := environment.TierProduction
	if _, err := b.Records.Write(context.Background(), records.Entry{Key: albpart.ConsumerAt(tier, "shop"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	groups, err := planProjectRemoval(context.Background(), b, tier, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Action != provider.ActionDelete || groups[0].Name != "gcp/acme-prod/production/shop" {
		t.Fatalf("removing shop plans %+v, want one group deleting what shop holds in the tier", groups)
	}
	if changes := groups[0].Changes; len(changes) != 1 || changes[0].Name != albpart.PartName {
		t.Errorf("removing shop plans %+v, want the alb part released", changes)
	}
	if groups, err := planProjectRemoval(context.Background(), b, tier, "blog"); err != nil || len(groups) != 0 {
		t.Errorf("removing blog, which holds nothing, plans %+v, %v", groups, err)
	}
}
