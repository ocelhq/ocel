package vps

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/target"
)

func TestAnUnsetComputeTakesTheBoxsStandingProcess(t *testing.T) {
	t.Parallel()

	held, err := providerkit.ConnectorCompute("", connectorCompute)
	if err != nil {
		t.Fatalf("providerkit.ConnectorCompute(\"\", connectorCompute) = %v, want the provider to pick for itself", err)
	}
	if held != providerkit.ComputeContainer {
		t.Errorf("providerkit.ConnectorCompute(\"\", connectorCompute) = %q, want %q", held, providerkit.ComputeContainer)
	}
}

func TestAComputeNoMachineHandsOutIsRefusedByTheProvider(t *testing.T) {
	t.Parallel()

	_, err := providerkit.ConnectorCompute(providerkit.ComputeServerless, connectorCompute)
	if err == nil {
		t.Fatal("ConnectorCompute(serverless) = nil, want a box to refuse a compute it cannot hand out")
	}
	if !strings.Contains(err.Error(), string(providerkit.ComputeServerless)) {
		t.Errorf("err = %v, want it to name the compute it refused", err)
	}
}

func TestADestinationTheConsoleCannotDialIsRefusedBeforeAnyRowIsWritten(t *testing.T) {
	t.Parallel()

	for _, held := range []string{"", "203.0.113.10", "2001:db8::1"} {
		if err := dialable(held); err == nil {
			t.Errorf("dialable(%q) = nil, want the describe that ocel connector add reads first to refuse", held)
		}
	}
	if err := dialable("box.example.com"); err != nil {
		t.Errorf("dialable(hostname) = %v, want a hostname taken", err)
	}
}

func TestTheHostKeyDigestStandsInATarget(t *testing.T) {
	t.Parallel()

	key := providerkit.HostKey{
		Type: "ssh-ed25519",
		Key:  base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 51)),
	}
	digest, err := hostKeyDigest(key)
	if err != nil {
		t.Fatalf("hostKeyDigest() = %v", err)
	}
	fingerprint, err := target.Fingerprint("vps", digest, "ocel")
	if err != nil {
		t.Fatalf("target.Fingerprint(vps, %q, ocel) = %v, want a host key a target can carry", digest, err)
	}
	if !strings.HasPrefix(digest, "sha256:") || fingerprint != "vps/"+digest+"/ocel" {
		t.Errorf("fingerprint = %q, want the digest to stand as one segment", fingerprint)
	}
}
