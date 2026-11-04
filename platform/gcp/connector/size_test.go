package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	providerMessageBytes = 128 << 20
	connectorBytes       = providerMessageBytes / 10 * 9
)

func TestTheReleasedConnectorFitsInOneMessageOverTheProviderChannel(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "connector")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > connectorBytes {
		t.Errorf("the released connector is %d bytes, over %d: `ocel connector add` sends the provider at most %d in one message, and a connector within a tenth of that has no room to grow",
			info.Size(), connectorBytes, providerMessageBytes)
	}
}
