package providerserver

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type heldDescribe struct {
	provider.Bootstrap
	entered chan struct{}
	release chan struct{}
}

func (h heldDescribe) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	close(h.entered)
	<-h.release
	return h.Bootstrap.Describe(ctx, tier)
}

func TestAStatusReadBeforeABootstrapWriteIsNotKeptAfterIt(t *testing.T) {
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	held := heldDescribe{Bootstrap: vendor.FakeBootstrap(), entered: make(chan struct{}), release: make(chan struct{})}
	statuses := &BootstrapStatuses{}
	gate := Gate{Bootstrap: held, KeyValues: vendor.KeyValues(), Statuses: statuses}

	read := make(chan error)
	go func() {
		_, err := gate.SessionStatus(context.Background(), environment.TierProduction)
		read <- err
	}()
	<-held.entered
	statuses.forget()
	close(held.release)
	if err := <-read; err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}

	if _, _, kept := statuses.find(bootstrapStatusKey{tier: environment.TierProduction}); kept {
		t.Error("a status read that began before a bootstrap write was kept after it, want the next read to go to the account")
	}
}
