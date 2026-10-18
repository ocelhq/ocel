package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type stallingStore struct {
	keyvalue.Store
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *stallingStore) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if s.armed.CompareAndSwap(true, false) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return keyvalue.Entry{}, ctx.Err()
		}
	}
	return s.Store.Read(ctx, key)
}

type stallingProvider struct {
	*fake.Provider
	store *stallingStore
}

func (p stallingProvider) KeyValues() keyvalue.Store { return p.store }

type shuttingDownServer struct {
	client   contractv1connect.ProviderServiceClient
	vendor   *fake.Provider
	store    *stallingStore
	shutDown context.CancelFunc
	returned <-chan error
}

func serveUntilShutdown(t *testing.T) *shuttingDownServer {
	t.Helper()
	vendor := fake.NewProvider(fake.Options{})
	store := &stallingStore{Store: vendor.KeyValues(), entered: make(chan struct{}), release: make(chan struct{})}
	mux := providerserver.ConformanceMux(providerserver.Config{Version: "test", New: func(context.Context, provider.Settings) (provider.Provider, error) {
		return stallingProvider{Provider: vendor, store: store}, nil
	}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, shutDown := context.WithCancel(context.Background())
	t.Cleanup(shutDown)
	returned := make(chan error, 1)
	go func() { returned <- providerserver.ServeOn(ctx, ln, mux, 5*time.Second, func() {}) }()
	client := contractv1connect.NewProviderServiceClient(http.DefaultClient, "http://"+ln.Addr().String())
	if _, err := client.Configure(context.Background(), configureInWorkingDir(t)); err != nil {
		t.Fatalf("Configure() = %v", err)
	}
	return &shuttingDownServer{client: client, vendor: vendor, store: store, shutDown: shutDown, returned: returned}
}

func (s *shuttingDownServer) shutDownDuring(t *testing.T, call func() error) error {
	t.Helper()
	s.store.armed.Store(true)
	answered := make(chan error, 1)
	go func() { answered <- call() }()
	select {
	case <-s.store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never reached the store")
	}
	s.shutDown()
	time.Sleep(200 * time.Millisecond)
	close(s.store.release)
	select {
	case err := <-answered:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the call never answered")
		return nil
	}
}

func TestAnAbandonDeployInFlightWhenTheProviderShutsDownStillFreesTheLease(t *testing.T) {
	served := serveUntilShutdown(t)
	recordLease(t, served.vendor.KeyValues(), environment.TierProduction, stackrecords.ProductionEnv, infraLease)

	err := served.shutDownDuring(t, func() error {
		_, err := served.client.AbandonDeploy(context.Background(), &contractv1.AbandonDeployRequest{
			Slug: "shop", Environment: deployRequest().GetEnvironment(), LeaseToken: infraLease,
		})
		return err
	})

	if err != nil {
		t.Fatalf("AbandonDeploy() in flight as the provider shut down = %v, want it to finish within the shutdown window", err)
	}
	if readEnvironmentLease(t, served.vendor) {
		t.Error("the environment still holds the abandoned deploy's lease, want it freed: a lease left behind blocks the next deploy for its whole TTL")
	}
}

func TestAForgetPreviewAliasInFlightWhenTheProviderShutsDownStillForgetsTheAlias(t *testing.T) {
	served := serveUntilShutdown(t)
	unclaimed, err := json.Marshal(stackrecords.EnvironmentMeta{AliasToken: "abcdefghijklmnop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := served.vendor.KeyValues().Write(context.Background(), keyvalue.Entry{Key: stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-7"), Value: unclaimed}); err != nil {
		t.Fatal(err)
	}

	err = served.shutDownDuring(t, func() error {
		_, err := served.client.ForgetPreviewAlias(context.Background(), &contractv1.ForgetPreviewAliasRequest{
			Slug: "shop", Environment: previewRequest().GetEnvironment(), Token: "abcdefghijklmnop",
		})
		return err
	})

	if err != nil {
		t.Fatalf("ForgetPreviewAlias() in flight as the provider shut down = %v, want it to finish within the shutdown window", err)
	}
	if _, err := served.vendor.KeyValues().Read(context.Background(), stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-7")); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading pr-7 after its alias was forgotten = %v, want it gone", err)
	}
}
