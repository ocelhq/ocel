package framework

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestWorkerFor_ResolvesNextOnCloudflare(t *testing.T) {
	assemble, err := WorkerFor(edge.FrameworkNext, edge.KindCloudflare)
	if err != nil || assemble == nil {
		t.Fatalf("WorkerFor(next, cloudflare) = %v, %v", assemble != nil, err)
	}
}

func TestNeedsWorker(t *testing.T) {
	if !NeedsWorker(edge.FrameworkNext) {
		t.Error("next is fronted by a worker")
	}
	if NeedsWorker("express") {
		t.Error("a framework registering nothing needs no worker")
	}
}

func TestWorkerFor_UnsupportedPairingNamesBoth(t *testing.T) {
	_, err := WorkerFor(edge.FrameworkNext, "provider-native")
	if err == nil {
		t.Fatal("expected an error for an unsupported pairing")
	}
	if !strings.Contains(err.Error(), "next") || !strings.Contains(err.Error(), "provider-native") {
		t.Errorf("error must name both framework and edge, got %q", err)
	}
}
