package providerkit_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

type hosting struct {
	*fake.Provider

	mu    sync.Mutex
	asked [][]string

	target  providerkit.RegistryTarget
	refusal error
}

func (h *hosting) ImageRegistry(_ context.Context, repositories []string) (providerkit.RegistryTarget, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, repositories)
	return h.target, h.refusal
}

func (h *hosting) repositories() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.asked
}

func registryServed(t *testing.T, provider providerkit.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()
	return servedProvider(t, "1.0.0", provider)
}

func TestAProviderWithNoRegistryOfItsOwnLeavesTheResolveUnimplemented(t *testing.T) {
	client := registryServed(t, fake.NewProvider(fake.Options{}))

	_, err := client.ResolveImageRegistry(context.Background(), &contractv1.ResolveImageRegistryRequest{
		Repositories: []string{"web"},
	})

	if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
		t.Errorf("ResolveImageRegistry() error code = %v, want %v: a provider that hosts no registry says so by not answering", got, connect.CodeUnimplemented)
	}
}

func TestAProviderWithARegistryAnswersItsCoordinatesAndCredentials(t *testing.T) {
	provider := &hosting{Provider: fake.NewProvider(fake.Options{}), target: providerkit.RegistryTarget{
		Server:    "registry.invalid",
		Namespace: "ocel/acme",
		Username:  "robot",
		Password:  "hunter2",
	}}
	client := registryServed(t, provider)

	resp, err := client.ResolveImageRegistry(context.Background(), &contractv1.ResolveImageRegistryRequest{
		Repositories: []string{"web", "api"},
	})
	if err != nil {
		t.Fatalf("ResolveImageRegistry() error = %v, want the provider's own registry", err)
	}

	if got := resp.GetServer(); got != "registry.invalid" {
		t.Errorf("ResolveImageRegistry() server = %q, want %q", got, "registry.invalid")
	}
	if got := resp.GetNamespace(); got != "ocel/acme" {
		t.Errorf("ResolveImageRegistry() namespace = %q, want %q", got, "ocel/acme")
	}
	if got := resp.GetUsername(); got != "robot" {
		t.Errorf("ResolveImageRegistry() username = %q, want %q", got, "robot")
	}
	if got := resp.GetPassword(); got != "hunter2" {
		t.Errorf("ResolveImageRegistry() password = %q, want the token the push logs in with", got)
	}
}

func TestTheProviderIsToldWhichRepositoriesTheDeployIntendsToPush(t *testing.T) {
	provider := &hosting{Provider: fake.NewProvider(fake.Options{}), target: providerkit.RegistryTarget{Server: "registry.invalid"}}
	client := registryServed(t, provider)

	if _, err := client.ResolveImageRegistry(context.Background(), &contractv1.ResolveImageRegistryRequest{
		Repositories: []string{"web", "api"},
	}); err != nil {
		t.Fatalf("ResolveImageRegistry() error = %v", err)
	}

	asked := provider.repositories()
	if len(asked) != 1 {
		t.Fatalf("the provider was asked %d times, want exactly one resolve per deploy", len(asked))
	}
	if strings.Join(asked[0], ",") != "web,api" {
		t.Errorf("the provider was asked for %v, want the repositories the deploy intends to push, so it can ensure they exist", asked[0])
	}
}

func TestAResolveNamingNoRepositoryNeverReachesTheProvider(t *testing.T) {
	provider := &hosting{Provider: fake.NewProvider(fake.Options{}), target: providerkit.RegistryTarget{Server: "registry.invalid"}}
	client := registryServed(t, provider)

	if _, err := client.ResolveImageRegistry(context.Background(), &contractv1.ResolveImageRegistryRequest{}); err == nil {
		t.Fatal("ResolveImageRegistry() with nothing to push succeeded, want a resolve to happen only when something is pushed")
	}
	if asked := provider.repositories(); len(asked) != 0 {
		t.Errorf("the provider was asked %v for a deploy that pushes nothing", asked)
	}
}

func TestARegistryWithNoServerIsRefusedRatherThanPassedOn(t *testing.T) {
	provider := &hosting{Provider: fake.NewProvider(fake.Options{}), target: providerkit.RegistryTarget{Namespace: "ocel/acme"}}
	client := registryServed(t, provider)

	_, err := client.ResolveImageRegistry(context.Background(), &contractv1.ResolveImageRegistryRequest{
		Repositories: []string{"web"},
	})
	if err == nil {
		t.Fatal("ResolveImageRegistry() answered a registry with no server, want the provider held to naming one")
	}
	if !strings.Contains(err.Error(), "server") {
		t.Errorf("ResolveImageRegistry() error = %v, and the provider author never learns which half is missing", err)
	}
}

func TestARegistryTheProviderRefusesToNameFailsTheResolve(t *testing.T) {
	provider := &hosting{Provider: fake.NewProvider(fake.Options{}), refusal: errors.New("the repository could not be created")}
	client := registryServed(t, provider)

	_, err := client.ResolveImageRegistry(context.Background(), &contractv1.ResolveImageRegistryRequest{
		Repositories: []string{"web"},
	})
	if err == nil {
		t.Fatal("ResolveImageRegistry() succeeded over a provider that refused, want the refusal carried")
	}
	if !strings.Contains(err.Error(), "the repository could not be created") {
		t.Errorf("ResolveImageRegistry() error = %v, want the provider's own reason", err)
	}
}
