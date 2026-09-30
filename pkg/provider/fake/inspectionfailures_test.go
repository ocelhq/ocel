package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestQueuedInspectionOutcomesAnswerTheNextInspectionsInTurn(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	failed := errors.New("the certificate service did not answer")
	p.QueueInspectionFailures(nil, failed)

	for i, want := range []error{nil, failed, nil} {
		if _, err := p.Certificates().Inspect(context.Background(), fake.KindRelay, "", "shop.example.com", provider.Certificate{}); !errors.Is(err, want) {
			t.Errorf("inspection %d err = %v, want %v", i+1, err, want)
		}
	}
}
