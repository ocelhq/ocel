package provider_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

const deploymentID = "0123456789abcdef0123456789abcdef"

func TestBuildRoundTripsThroughItsRenderedForm(t *testing.T) {
	t.Parallel()

	built, err := provider.NewBuild(deploymentID, "prod", "v1")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := provider.ParseBuild(built.String())
	if err != nil {
		t.Fatalf("ParseBuild(%q) = %v", built, err)
	}
	if parsed != built {
		t.Errorf("ParseBuild(%q) = %v, want the build it rendered", built, parsed)
	}
	if parsed.Release() != built.Release() {
		t.Errorf("the parsed build releases to %s, want %s", parsed.Release(), built.Release())
	}
}

func TestNewBuildRefusesADeploymentIdNothingCanName(t *testing.T) {
	t.Parallel()

	if _, err := provider.NewBuild("not-a-deployment-id", "prod", ""); err == nil {
		t.Fatal("NewBuild() with a malformed deployment id succeeded, want a refusal")
	}
	if _, err := provider.NewBuild(deploymentID, "", ""); err == nil {
		t.Fatal("NewBuild() with no environment succeeded, want a refusal: the fingerprint is scoped to one")
	}
}
