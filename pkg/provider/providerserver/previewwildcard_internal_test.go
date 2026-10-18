package providerserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type alwaysMoving struct{ *fake.KeyValues }

func (alwaysMoving) Write(context.Context, records.Entry) (records.Revision, error) {
	return "", records.ErrStale
}

func TestASharedPreviewEntryThatMovesUnderEveryWriteIsGivenUpOnAsContended(t *testing.T) {
	t.Parallel()

	w := &wildcards{keyValues: alwaysMoving{fake.NewKeyValues()}, recorded: stackrecords.Wildcard{BaseDomain: "previews.example.com"}}
	if err := w.save(context.Background()); !errors.Is(err, records.ErrContended) {
		t.Errorf("save() = %v, want it given up on as contended", err)
	}
	err := w.swapOriginCertificate(context.Background(), "", "issued", time.Now())
	if !errors.Is(err, records.ErrContended) || !errors.Is(err, errPreviewEntryRenewing) {
		t.Errorf("swapOriginCertificate() = %v, want it given up on as contended, naming the renewal", err)
	}
}
