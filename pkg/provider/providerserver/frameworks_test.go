package providerserver_test

import (
	"slices"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func TestAContainerNextAppRequiresNoFeatureAServerlessNextAppDoes(t *testing.T) {
	t.Parallel()

	next := &contractv1.Framework{Name: "next"}
	serverless := &contractv1.ManifestApp{
		Name:      "site",
		Framework: next,
		Artifact:  &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}},
	}
	container := &contractv1.ManifestApp{
		Name:      "web",
		Framework: next,
		Artifact:  &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{Image: "ocel/web@sha256:1"}},
	}
	catalogue := fake.NewBootstrap().Catalogue()

	required := func(apps ...*contractv1.ManifestApp) []string {
		frameworks := providerserver.FrameworksOf(&contractv1.Manifest{Apps: apps})
		features, err := bootstrapplan.RequiredFeatures(catalogue, frameworks, "")
		if err != nil {
			t.Fatalf("RequiredFeatures: %v", err)
		}
		return features
	}

	if got := required(serverless, container); !slices.Contains(got, fake.FeatureCache) {
		t.Errorf("features of a serverless and a container Next app = %v, want %q required for the functions", got, fake.FeatureCache)
	}
	if got := required(container); len(got) != 0 {
		t.Errorf("features of a container-only Next app = %v, want none: features keyed on a framework are for its functions", got)
	}
}
