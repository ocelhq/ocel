package provider_test

import (
	"slices"
	"strings"
	"testing"

	"buf.build/go/protovalidate"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestEveryArtifactAManifestAppCarriesNamesACompute(t *testing.T) {
	apps := []*contractv1.ManifestApp{
		{Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}}},
		{Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}},
	}
	cases := (&contractv1.ManifestApp{}).ProtoReflect().Descriptor().Oneofs().ByName("artifact").Fields().Len()
	if cases != len(apps) {
		t.Fatalf("ManifestApp.artifact has %d cases and this test reads %d, so a case would reach a provider with no compute it runs", cases, len(apps))
	}

	named := make([]provider.Compute, 0, len(apps))
	for _, app := range apps {
		named = append(named, provider.ComputeOf(app))
	}
	slices.Sort(named)
	known := slices.Sorted(slices.Values(provider.Computes()))
	if !slices.Equal(named, known) {
		t.Errorf("the artifacts a ManifestApp carries name computes %v, Computes() names %v", named, known)
	}
}

func TestTheWireRefusesAnAppThatCarriesNoArtifact(t *testing.T) {
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(&contractv1.ManifestApp{Name: "web"}); err == nil {
		t.Fatal("a ManifestApp with neither functions nor a container image validated, so a provider would be handed an app with nothing to run")
	}
}

func TestConnectorComputeDefaultsToTheFirstSupported(t *testing.T) {
	compute, err := provider.ConnectorCompute("", provider.ComputeContainer, provider.ComputeServerless)
	if err != nil {
		t.Fatalf("ConnectorCompute(\"\", container, serverless) = %v, want the target to pick for itself", err)
	}
	if compute != provider.ComputeContainer {
		t.Errorf("ConnectorCompute(\"\", container, serverless) = %q, want %q", compute, provider.ComputeContainer)
	}
}

func TestConnectorComputeTakesAnySupported(t *testing.T) {
	compute, err := provider.ConnectorCompute(provider.ComputeServerless, provider.ComputeContainer, provider.ComputeServerless)
	if err != nil {
		t.Fatalf("ConnectorCompute(serverless, container, serverless) = %v, want it taken", err)
	}
	if compute != provider.ComputeServerless {
		t.Errorf("ConnectorCompute(serverless, ...) = %q, want %q", compute, provider.ComputeServerless)
	}
}

func TestConnectorComputeRefusesWhatTheTargetDoesNotHandOut(t *testing.T) {
	_, err := provider.ConnectorCompute(provider.ComputeContainer, provider.ComputeServerless)
	if err == nil {
		t.Fatal("ConnectorCompute(container, serverless) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), string(provider.ComputeServerless)) {
		t.Errorf("ConnectorCompute(container, serverless) = %q, want it to name what the target does run", err)
	}
}

func TestConnectorComputeRefusesATargetThatSupportsNothing(t *testing.T) {
	if _, err := provider.ConnectorCompute(""); err == nil {
		t.Fatal("ConnectorCompute(\"\") = nil, want a refusal")
	}
}
