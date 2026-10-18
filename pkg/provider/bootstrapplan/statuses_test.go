package bootstrapplan_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type heldDescribe struct {
	provider.Bootstrap
	entered chan struct{}
	release chan struct{}
	reads   *atomic.Int32
}

func (h heldDescribe) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	if h.reads.Add(1) == 1 {
		close(h.entered)
		<-h.release
	}
	return h.Bootstrap.Describe(ctx, tier)
}

func TestAStatusReadBeforeABootstrapWriteIsNotKeptAfterIt(t *testing.T) {
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	held := heldDescribe{Bootstrap: vendor.FakeBootstrap(), entered: make(chan struct{}), release: make(chan struct{}), reads: &atomic.Int32{}}
	statuses := &bootstrapplan.Statuses{}
	readiness := bootstrapplan.Readiness{Bootstrap: held, KeyValues: vendor.KeyValues(), Statuses: statuses}

	read := make(chan error)
	go func() {
		_, err := readiness.SessionStatus(context.Background(), environment.TierProduction)
		read <- err
	}()
	<-held.entered
	statuses.Forget()
	close(held.release)
	if err := <-read; err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}

	if _, err := readiness.SessionStatus(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	if reads := held.reads.Load(); reads != 2 {
		t.Errorf("the account was read %d times, want 2: a status read that began before a bootstrap write was kept after it, so the next read never went to the account", reads)
	}
}
