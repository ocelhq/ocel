package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

const stripeKey = "sk_live_hidden"

func withValues(spec provider.StackSpec, sensitive map[string]string) provider.StackSpec {
	values := provider.AppValues{Plain: map[string]string{"MODE": "live"}, Sensitive: sensitive, ContainerEnv: map[string]string{"MODE": "live"}}
	for key, value := range sensitive {
		values.ContainerEnv[key] = value
	}
	spec.App.Values = values
	return spec
}

func valueSecretOf(t *testing.T, p *Provider, spec provider.StackSpec, key string) string {
	t.Helper()
	return must(namesOf(t, p).AppValueSecret(spec.Ref.Project, spec.Ref.Name.Env, spec.App.App, key))
}

func envEntry(container *runv2.GoogleCloudRunV2Container, name string) *runv2.GoogleCloudRunV2EnvVar {
	for _, entry := range container.Env {
		if entry.Name == name {
			return entry
		}
	}
	return nil
}

func wantPinned(t *testing.T, service *runv2.GoogleCloudRunV2Service, name, secret, version string) {
	t.Helper()
	entry := envEntry(service.Template.Containers[0], name)
	if entry == nil || entry.Value != "" || entry.ValueSource == nil || entry.ValueSource.SecretKeyRef == nil ||
		entry.ValueSource.SecretKeyRef.Secret != secret || entry.ValueSource.SecretKeyRef.Version != version {
		t.Errorf("%s of %s = %+v, want a reference to version %s of %s and no value", name, service.Name, entry, version, secret)
	}
}

func wantNoLiteral(t *testing.T, service *runv2.GoogleCloudRunV2Service, value string) {
	t.Helper()
	encoded, err := json.Marshal(service)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), value) {
		t.Errorf("the spec of %s holds %q itself, and any credential that may list the project's services reads it", service.Name, value)
	}
}

func TestAContainersSensitiveValueReachesItFromSecretManagerAndNeverAsALiteral(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := withValues(previewSpec(), map[string]string{"STRIPE_KEY": stripeKey})

	if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	secret := valueSecretOf(t, p, spec, "STRIPE_KEY")
	wantPinned(t, server.Service, "STRIPE_KEY", secret, "1")
	wantNoLiteral(t, server.Service, stripeKey)
	if plain := envEntry(server.Service.Template.Containers[0], "MODE"); plain == nil || plain.Value != "live" {
		t.Errorf("MODE = %+v, want the plain value as it is", plain)
	}
	if held, _ := server.Secrets.Latest(t, secret); held != stripeKey {
		t.Errorf("the secret %s holds %q, want the value", secret, held)
	}
}

func TestEveryFunctionOfAnAppReadsItsSensitiveValueFromTheSameSecretVersion(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := previewSpec()
	spec.App.Compute = provider.ComputeServerless
	spec.App.Functions = append(spec.App.Functions, provider.FunctionSpec{Name: "fn--web--orders", Framework: spec.App.Functions[0].Framework})
	spec.Images.Pushes = append(spec.Images.Pushes, provider.ImagePush{App: "fn--web--orders", ImageRef: "europe-west1-docker.pkg.dev/acme/ocel/web-orders@sha256:abc", Function: true})
	spec = withValues(spec, map[string]string{"STRIPE_KEY": stripeKey})

	if _, err := p.ProvisionFunctions(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	secret := valueSecretOf(t, p, spec, "STRIPE_KEY")
	released := append(slices.Clone(server.Created), server.Patched...)
	if len(released) != 2 {
		t.Fatalf("the release wrote %d services, want one per function", len(released))
	}
	for _, service := range released {
		wantPinned(t, service, "STRIPE_KEY", secret, "1")
		wantNoLiteral(t, service, stripeKey)
	}
	if added := server.Secrets.VersionsAdded(); added != 1 {
		t.Errorf("the release wrote %d versions, want one that every function pins", added)
	}
}

func TestAnAppsAccountMayReadItsOwnValueSecretsAndNoOtherAppsAccountMay(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	web := withValues(previewSpec(), map[string]string{"STRIPE_KEY": stripeKey})
	api := withValues(appNamed("api", previewSpec()), map[string]string{"STRIPE_KEY": "sk_api"})

	for _, spec := range []provider.StackSpec{web, api} {
		if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
			t.Fatalf("ProvisionContainers(%s) = %v", spec.App.App, err)
		}
	}

	webAccount := "serviceAccount:" + namesOf(t, p).AppAccountEmail("shop", "pr-7", "web")
	apiAccount := "serviceAccount:" + namesOf(t, p).AppAccountEmail("shop", "pr-7", "api")
	readers := server.Secrets.Members(valueSecretOf(t, p, web, "STRIPE_KEY"), release.ValueReaderRole)
	if !slices.Equal(readers, []string{webAccount}) {
		t.Errorf("the web app's value secret lets %v read it, want %s alone and never %s", readers, webAccount, apiAccount)
	}
}

func TestAnUnchangedSensitiveValueKeepsItsVersionAndAChangedOneIsPinnedAtANewOne(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := withValues(previewSpec(), map[string]string{"STRIPE_KEY": stripeKey})
	secret := valueSecretOf(t, p, spec, "STRIPE_KEY")

	for range 2 {
		if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
			t.Fatalf("ProvisionContainers() = %v", err)
		}
	}
	wantPinned(t, server.Service, "STRIPE_KEY", secret, "1")
	if added := server.Secrets.VersionsAdded(); added != 1 {
		t.Errorf("two releases of the same value wrote %d versions, want one", added)
	}

	if _, err := p.ProvisionContainers(context.Background(), withValues(previewSpec(), map[string]string{"STRIPE_KEY": "sk_rotated"}), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	wantPinned(t, server.Service, "STRIPE_KEY", secret, "2")
	if enabled := server.Secrets.EnabledVersions(secret); !slices.Equal(enabled, []int{1, 2}) {
		t.Errorf("enabled versions = %v, want 1 kept for the release that shipped with it, and 2", enabled)
	}
}

func TestRetiringAReleaseDestroysTheValueVersionsNoRemainingRevisionPins(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	released := newReleasedStacks(p)
	image := "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one"
	first := withValues(functionRelease("d1", image), map[string]string{"STRIPE_KEY": "one", "RETIRED_KEY": "gone"})
	one := released.provision(t, first)
	active := released.provision(t, withValues(functionRelease("d2", image), map[string]string{"STRIPE_KEY": "two"}))
	if _, err := p.services().Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	if _, err := p.RemoveFunctionRevisions(context.Background(), first.Ref, []provider.Function{one}, nil, &fake.Log{}); err != nil {
		t.Fatalf("RemoveFunctionRevisions() = %v", err)
	}

	stripe := valueSecretOf(t, p, first, "STRIPE_KEY")
	if enabled := server.Secrets.EnabledVersions(stripe); !slices.Equal(enabled, []int{2}) {
		t.Errorf("enabled versions of %s = %v, want only 2, which the active release pins", stripe, enabled)
	}
	if retired := valueSecretOf(t, p, first, "RETIRED_KEY"); server.Secrets.Has(retired) {
		t.Errorf("%s is still there, and no remaining release declares or pins it", retired)
	}
}

func TestDestroyingTheLastReleaseOfAnAppEnvironmentDeletesItsValueSecretsAfterTheirTierTag(t *testing.T) {
	tags := googletest.NewTieredTags(t)
	server := &googletest.Run{Secrets: googletest.NewSecrets()}
	server.Secrets.TierTags = tags
	p := providerServing(server, t)
	tags.Tagged(p.resolved)
	released := newReleasedStacks(p)
	only := withValues(functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one"), map[string]string{"STRIPE_KEY": stripeKey})
	released.provision(t, only)
	secret := valueSecretOf(t, p, only, "STRIPE_KEY")
	if !server.Secrets.Has(secret) {
		t.Fatalf("the release made no secret %s", secret)
	}

	released.destroy(t, only.Ref)

	if server.Secrets.Has(secret) {
		t.Errorf("%s outlived every release of its app environment", secret)
	}
	if len(server.Secrets.DeletedTagged) != 0 {
		t.Errorf("%v were deleted while still bound to their tier's tag, which then blocks deleting the tag value", server.Secrets.DeletedTagged)
	}
}

func TestAValueSecretCarriesItsTiersTagFromItsCreation(t *testing.T) {
	tags := googletest.NewTieredTags(t)
	server := &googletest.Run{Secrets: googletest.NewSecrets()}
	server.Secrets.TierTags = tags
	p := providerServing(server, t)
	tags.Tagged(p.resolved)
	spec := withValues(previewSpec(), map[string]string{"STRIPE_KEY": stripeKey})

	if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	secret := valueSecretOf(t, p, spec, "STRIPE_KEY")
	if got := server.Secrets.InlineTags[secret]; got[p.resolved.TierTagKeyPath()] != string(spec.Ref.Tier) {
		t.Errorf("%s was created with tags %v, want its tier's value inline", secret, got)
	}
}

func TestAnEmptySensitiveValueIsSetAsItIsSinceSecretManagerHoldsNoEmptyPayload(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := withValues(previewSpec(), map[string]string{"STRIPE_KEY": stripeKey, "UNSET_KEY": ""})

	if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	if entry := envEntry(server.Service.Template.Containers[0], "UNSET_KEY"); entry == nil || entry.Value != "" || entry.ValueSource != nil {
		t.Errorf("UNSET_KEY = %+v, want it set empty: there is nothing to hide, and Secret Manager refuses a version with no payload", entry)
	}
	if server.Secrets.Has(valueSecretOf(t, p, spec, "UNSET_KEY")) {
		t.Error("the empty value was given a secret")
	}
}

func TestAnEmulatedServiceTakesItsSensitiveValuesAsLiterals(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	p.sensitiveInEnv = true
	spec := withValues(previewSpec(), map[string]string{"STRIPE_KEY": stripeKey})

	if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	if got := googletest.EnvOf(server.Service.Template.Containers[0])["STRIPE_KEY"]; got != stripeKey {
		t.Errorf("STRIPE_KEY = %q, want the value itself: floci-gcp refuses an env valueSource", got)
	}
}

func valueSecretsRecordOf(t *testing.T, p *Provider, spec provider.StackSpec) records.Key {
	t.Helper()
	account := namesOf(t, p).AppAccount(spec.Ref.Project, spec.Ref.Name.Env, spec.App.App)
	return records.Partition{Tier: spec.Ref.Tier, Root: stackrecords.RootBootstrap}.Key("appvaluesecrets", account)
}

func TestDestroyingEveryReleaseOfAnAppEnvironmentLeavesNoRecordOfItsValueSecrets(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	released := newReleasedStacks(p)
	image := "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one"
	first := withValues(functionRelease("d1", image), map[string]string{"STRIPE_KEY": stripeKey})
	second := functionRelease("d2", image)
	released.provision(t, first)
	active := released.provision(t, second)
	if _, err := p.services().Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	released.destroy(t, first.Ref)
	released.destroy(t, second.Ref)

	key := valueSecretsRecordOf(t, p, first)
	if recorded, err := released.store.Read(context.Background(), key); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("%s holds %q after every release of its app environment was destroyed, want it gone", key, recorded.Value)
	}
}
