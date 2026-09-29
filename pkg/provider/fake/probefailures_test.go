package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAQueuedProbeFailureFailsTheNextProbeOfThatHostnameAndIsReportedAsTheLastFailure(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	refused := errors.New("shop.example.com refused the connection")
	p.QueueProbeFailures("shop.example.com", refused)

	if _, err := p.Liveness().ServingRouter(context.Background(), "blog.example.com"); err != nil {
		t.Fatalf("probe of another hostname err = %v, want it unaffected", err)
	}
	if _, err := p.Liveness().ServingRouter(context.Background(), "shop.example.com"); !errors.Is(err, refused) {
		t.Fatalf("first probe err = %v, want the queued failure", err)
	}
	if last := p.Liveness().LastProbeFailure("shop.example.com"); last != refused.Error() {
		t.Errorf("last probe failure = %q, want %q", last, refused)
	}
	if _, err := p.Liveness().ServingRouter(context.Background(), "shop.example.com"); err != nil {
		t.Fatalf("second probe err = %v, want the queue spent", err)
	}
	if last := p.Liveness().LastProbeFailure("shop.example.com"); last != "" {
		t.Errorf("last probe failure after an answered probe = %q, want none", last)
	}
}
