package gcp

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func TestEveryCloudRunServiceAProjectReleasesIsLabelledWithItsProject(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)

	if _, err := p.ProvisionContainers(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if _, err := p.ProvisionFunctions(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	released := slices.Concat(server.Created, server.Patched)
	if len(released) != 2 {
		t.Fatalf("released %d services, want the container and the function", len(released))
	}
	want := map[string]string{
		names.NamespaceLabel:   string(namesOf(t, p).Namespace()),
		names.ProjectLabel:     "shop",
		names.TierLabel:        "preview",
		names.EnvironmentLabel: "pr-7",
	}
	for _, service := range released {
		for key, value := range want {
			if got := service.Labels[key]; got != value {
				t.Errorf("a service of project shop is labelled %s=%q, want %q: what sweeps a project's services finds them by its labels", key, got, value)
			}
		}
		if service.Labels[run.OpensOnPromotionLabel] == "" {
			t.Errorf("the service is labelled %v, want it still to say how a promotion opens it", service.Labels)
		}
	}
}

func TestEveryCloudRunServiceThatServesAnAppIsLabelledWithItsApp(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)

	if _, err := p.ProvisionContainers(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if _, err := p.ProvisionFunctions(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	released := slices.Concat(server.Created, server.Patched)
	if len(released) != 2 {
		t.Fatalf("released %d services, want the container and the function", len(released))
	}
	for _, service := range released {
		if got := service.Labels[names.AppLabel]; got != "web" {
			t.Errorf("a service serving app web is labelled %s=%q, want web: the e2e harness finds where an app is served by this label", names.AppLabel, got)
		}
	}
}

func TestAWorkerServiceIsLabelledWithNoApp(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	p := providerServing(server, t)
	spec := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	spec.App.Workers = []provider.WorkerSpec{{Name: "media"}}
	account, err := p.release().EnsureAccount(context.Background(), p.resolved, spec, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := p.release().ProvisionWorkers(context.Background(), p.resolved, spec, "europe-west1-docker.pkg.dev/acme/ocel/api@sha256:abc", account, nil, nil, provider.Instances{}, nil); err != nil {
		t.Fatalf("provisionWorkers() = %v", err)
	}

	if len(server.Created) == 0 {
		t.Fatal("provisionWorkers() created no service")
	}
	for _, service := range server.Created {
		if got, labelled := service.Labels[names.AppLabel]; labelled {
			t.Errorf("a worker service is labelled %s=%q, want no app label: only a service that serves the app carries it", names.AppLabel, got)
		}
		if got := service.Labels[names.ProjectLabel]; got != "shop" {
			t.Errorf("a worker service is labelled %s=%q, want shop", names.ProjectLabel, got)
		}
	}
}

func TestALabelValueThatFitsIsKeptAndALongerOneIsCutAndTaggedWithItsHash(t *testing.T) {
	if got := must(names.LabelValue("feature-login-page-v1")); got != "feature-login-page-v1" {
		t.Errorf("LabelValue() = %q, want a value that fits kept as it is", got)
	}
	if got, want := must(names.LabelValue(strings.Repeat("a", 70))), strings.Repeat("a", 55)+"-6bd5e5"; got != want {
		t.Errorf("LabelValue() = %q, want %q: the head that fits and the hash tag of the whole value", got, want)
	}
}

func TestALabelValueIsOneGoogleCloudTakes(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z0-9_-]{0,63}$`)
	ref := provider.StackRef{Project: "Shop " + strings.Repeat("a", 80), Tier: "production"}
	ref.Name.Env = "prod"

	labels := must(names.StackLabels(names.New(provider.Namespace("ocel"), ""), ref))

	for key, value := range labels {
		if !valid.MatchString(value) {
			t.Errorf("label %s=%q is not lowercase letters, digits, underscores and dashes of at most 63 characters", key, value)
		}
	}
	if other := must(names.StackLabels(names.New(provider.Namespace("ocel"), ""), provider.StackRef{Project: "Shop " + strings.Repeat("a", 81)})); other[names.ProjectLabel] == labels[names.ProjectLabel] {
		t.Errorf("two projects cut to one label %q", other[names.ProjectLabel])
	}
}
