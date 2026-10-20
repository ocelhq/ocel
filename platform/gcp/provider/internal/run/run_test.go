package run

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestATiersServiceCarriesItsTierOnEveryRevisionSoItsLogEntriesCarryIt(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		service, err := ServiceOf(Serving{Service: "ocel-app-web", Tier: tier, Image: "europe-west1-docker.pkg.dev/acme-prod/ocel/web@sha256:abc"})
		if err != nil {
			t.Fatal(err)
		}
		if got := service.Template.Labels[names.TierLabel]; got != string(tier) {
			t.Errorf("a %s service's revisions carry %s=%q, want %q: Cloud Run copies a revision's labels, not the service's, onto its log entries", tier, names.TierLabel, got, tier)
		}
		if got := service.Labels[names.TierLabel]; got != string(tier) {
			t.Errorf("a %s service carries %s=%q, want %q", tier, names.TierLabel, got, tier)
		}
	}
}

func TestASharedServiceCarriesNoTier(t *testing.T) {
	t.Parallel()

	service, err := ServiceOf(Serving{Service: "ocel-connector", Shared: true, Image: "europe-west1-docker.pkg.dev/acme-prod/ocel/connector@sha256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if got, carried := service.Template.Labels[names.TierLabel]; carried {
		t.Errorf("a shared service's revisions carry %s=%q, and its logs belong to neither tier's view", names.TierLabel, got)
	}
	if got, carried := service.Labels[names.TierLabel]; carried {
		t.Errorf("a shared service carries %s=%q", names.TierLabel, got)
	}
}

func TestAServiceReleasedWithoutItsTierLabelHasDrifted(t *testing.T) {
	t.Parallel()

	desired, err := ServiceOf(Serving{Service: "ocel-app-web", Tier: environment.TierPreview, Image: "europe-west1-docker.pkg.dev/acme-prod/ocel/web@sha256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	current := readBack(t, desired)
	delete(current.Template.Labels, names.TierLabel)
	if !Drifted(current, desired) {
		t.Error("a service whose revisions lack the tier label reads as current, so its logs stay out of the tier's log view")
	}
}
