package providerkit_test

import (
	"testing"

	connect "connectrpc.com/connect"
)

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
