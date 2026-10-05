package gcp

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestEveryCloudRunServiceAProjectReleasesIsLabelledWithItsProject(t *testing.T) {
	server := &runServer{}
	p := server.open(t)

	if _, err := p.ProvisionContainers(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if _, err := p.ProvisionFunctions(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	released := slices.Concat(server.created, server.patched)
	if len(released) != 2 {
		t.Fatalf("released %d services, want the container and the function", len(released))
	}
	want := map[string]string{
		namespaceLabel:   string(names(t, p).Namespace()),
		projectLabel:     "shop",
		tierLabel:        "preview",
		environmentLabel: "pr-7",
	}
	for _, service := range released {
		for key, value := range want {
			if got := service.Labels[key]; got != value {
				t.Errorf("a service of project shop is labelled %s=%q, want %q: what sweeps a project's services finds them by its labels", key, got, value)
			}
		}
		if service.Labels[opensOnPromotionLabel] == "" {
			t.Errorf("the service is labelled %v, want it still to say how a promotion opens it", service.Labels)
		}
	}
}

func TestARealtimeGatewayIsLabelledWithTheProjectItServes(t *testing.T) {
	service := gatewayService(t, "gateway.run.app")

	if got := service.Labels[projectLabel]; got != "shop" {
		t.Errorf("the gateway of project shop is labelled %s=%q, want shop", projectLabel, got)
	}
}

func TestALabelValueIsOneGoogleCloudTakes(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z0-9_-]{0,63}$`)
	ref := provider.StackRef{Project: "Shop " + strings.Repeat("a", 80), Tier: "production"}
	ref.Name.Env = "prod"

	labels := stackLabels(Names{namespace: provider.Namespace("ocel")}, ref)

	for key, value := range labels {
		if !valid.MatchString(value) {
			t.Errorf("label %s=%q is not lowercase letters, digits, underscores and dashes of at most 63 characters", key, value)
		}
	}
	if other := stackLabels(Names{namespace: provider.Namespace("ocel")}, provider.StackRef{Project: "Shop " + strings.Repeat("a", 81)}); other[projectLabel] == labels[projectLabel] {
		t.Errorf("two projects cut to one label %q", other[projectLabel])
	}
}
