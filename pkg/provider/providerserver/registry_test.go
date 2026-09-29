package providerserver_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type hosting struct {
	*fake.Provider

	mu    sync.Mutex
	tiers []environment.Tier

	target  provider.RegistryTarget
	refusal error
}

func (h *hosting) Hooks() provider.Hooks {
	hooks := h.Provider.Hooks()
	hooks.EnsureImageRegistry = h.EnsureImageRegistry
	return hooks
}

func (h *hosting) EnsureImageRegistry(_ context.Context, tier environment.Tier) (provider.RegistryTarget, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tiers = append(h.tiers, tier)
	return h.target, h.refusal
}

func (h *hosting) asking() []environment.Tier {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tiers
}

var ownRegistry = provider.RegistryTarget{
	Server:    "registry.invalid",
	Namespace: "ocel/acme",
	Username:  "robot",
	Password:  "own-token",
}

func hostingServed(t *testing.T, target provider.RegistryTarget, refusal error) (contractv1connect.ProviderServiceClient, *hosting) {
	t.Helper()
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	vendor := &hosting{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}), target: target, refusal: refusal}
	return servedBy(t, vendor), vendor
}

func TestADeployNamingNoRegistryPushesToTheProvidersOwn(t *testing.T) {
	client, vendor := hostingServed(t, ownRegistry, nil)

	result, events := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	opened := vendor.ImageStore().Opened()
	if len(opened) != 1 || opened[0] != ownRegistry {
		t.Fatalf("the registry was opened as %v, want the provider's own, resolved inside the deploy", opened)
	}
	pushed := vendor.ImageStore().Pushed()
	if len(pushed) != 1 || !strings.HasPrefix(pushed[0].ImageRef, "registry.invalid/ocel/acme/web:") {
		t.Errorf("the deploy pushed %v, want the image under the provider's own registry", pushed)
	}
	for _, event := range events {
		if encodingContains(t, event, ownRegistry.Password) {
			t.Fatal("the deploy stream contains the provider's own registry password")
		}
	}
}

func TestTheProvidersOwnRegistryIsResolvedForTheTierTheDeployTargets(t *testing.T) {
	client, vendor := hostingServed(t, ownRegistry, nil)

	req := containerDeployRequest("/")
	req.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"}
	_, _, _ = deployStream(t, client, req)

	if asking := vendor.asking(); len(asking) != 1 || asking[0] != environment.TierPreview {
		t.Errorf("the provider resolved a registry for %v, want %v: a tier keeps its images apart from the other tier's",
			asking, environment.TierPreview)
	}
}

func TestARegistryTheProjectNamesIsUsedWithoutAskingTheProvider(t *testing.T) {
	client, vendor := hostingServed(t, ownRegistry, nil)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if asking := vendor.asking(); len(asking) != 0 {
		t.Errorf("the provider resolved its own registry %v times for a deploy that names one", len(asking))
	}
	opened := vendor.ImageStore().Opened()
	if len(opened) != 1 || opened[0].Server != "ghcr.io" || opened[0].Password != "hunter2" {
		t.Errorf("the registry was opened as %v, want the one the deploy named", opened)
	}
}

func TestAProviderHostingNoRegistryForThisDeployLeavesItWithNone(t *testing.T) {
	client, vendor := hostingServed(t, provider.RegistryTarget{}, nil)

	req := containerDeployRequest("/")
	req.Dry = true
	result, _, err := deployStream(t, client, req)
	if err == nil && result.GetSuccess() {
		t.Fatal("Deploy() succeeded where neither side names a registry and the provider takes no image directly")
	}
	if len(vendor.asking()) != 1 {
		t.Errorf("the provider was asked %d times, want once", len(vendor.asking()))
	}
	if opened := vendor.ImageStore().Opened(); len(opened) != 0 {
		t.Errorf("a registry was opened as %v where the provider answered none", opened)
	}
}

func TestAnOwnRegistryWithNoServerIsRefusedRatherThanUsed(t *testing.T) {
	client, _ := hostingServed(t, provider.RegistryTarget{Namespace: "ocel/acme"}, nil)

	result, _, err := deployStream(t, client, containerDeployRequest("/"))
	if err == nil && result.GetSuccess() {
		t.Fatal("Deploy() used a registry with no server, want the provider required to name one")
	}
	refused := result.GetError()
	if err != nil {
		refused = err.Error()
	}
	if !strings.Contains(refused, "server") {
		t.Errorf("Deploy() = %q, and the provider author never learns which half is missing", refused)
	}
}

func TestAnOwnRegistryTheProviderCannotResolveFailsTheDeploy(t *testing.T) {
	client, _ := hostingServed(t, provider.RegistryTarget{}, errors.New("the login could not be minted"))

	result, _, err := deployStream(t, client, containerDeployRequest("/"))
	if err == nil && result.GetSuccess() {
		t.Fatal("Deploy() succeeded over a provider that could not resolve its registry")
	}
	refused := result.GetError()
	if err != nil {
		refused = err.Error()
	}
	if !strings.Contains(refused, "the login could not be minted") {
		t.Errorf("Deploy() = %q, want the provider's own reason", refused)
	}
}
