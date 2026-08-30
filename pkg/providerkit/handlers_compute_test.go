package providerkit_test

import (
	"testing"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheWireAcceptsAnAppNamingContainer(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Manifest.Apps[0].Compute = string(providerkit.ComputeContainer)
	req.Manifest.Containers = []*contractv1.ManifestContainer{{
		App:             req.Manifest.Apps[0].GetName(),
		Image:           "ocel/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		HealthCheckPath: "/",
	}}

	result, _, err := deployStream(t, client, req)
	if err != nil {
		t.Fatalf("Deploy() with an app naming %q: error = %v, want the wire pin to admit it — it is the only compute the VPS provider runs", providerkit.ComputeContainer, err)
	}
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() with an app naming %q = %q, want it to succeed", providerkit.ComputeContainer, result.GetError())
	}
}

func TestTheWireRefusesAnAppThatNamesNoCompute(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	req := deployRequest()
	req.Manifest.Apps[0].Compute = ""

	_, _, err := deployStream(t, client, req)
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with an app naming no compute: code = %v, want %v — the manifest pin is what makes compute non-empty by the time a provider reads it", got, connect.CodeInvalidArgument)
	}
}

func TestTheWireRefusesAnAppNamingAComputeOutsideTheVocabulary(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	req := deployRequest()
	req.Manifest.Apps[0].Compute = "vm"

	_, _, err := deployStream(t, client, req)
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with an app naming %q: code = %v, want %v", "vm", got, connect.CodeInvalidArgument)
	}
}
