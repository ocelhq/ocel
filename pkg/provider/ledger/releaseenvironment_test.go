package ledger

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/router"
)

func TestTheLedgerKeepsTheEnvironmentAReleaseWasStagedFor(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "web-p1", Environment: "pr-7"}); err != nil {
		t.Fatal(err)
	}
	record, found, err := l.Record(ctx, "web", "web-p1")
	if err != nil || !found || record.Environment != "pr-7" {
		t.Errorf("Record(web, web-p1) = (%+v, %v, %v), want it naming pr-7", record, found, err)
	}
	if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "web-p1", Environment: "pr-8"}); err == nil {
		t.Error("staging the same release for pr-8 = nil, want it refused: the record names pr-7")
	}
}
