package vps_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/conformance"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func storeless(t *testing.T) *vps.Provider {
	t.Helper()
	return vps.NewProvider(vps.TestAssets, vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
}

func TestTheProviderSaysItKeepsNoArtifactStore(t *testing.T) {
	t.Parallel()

	if storeless(t).Facts().StoresArtifacts {
		t.Fatal("Facts().StoresArtifacts = true, but a container app puts nothing in the store")
	}
}

func TestTheArtifactPortRunsTheConformancePortTier(t *testing.T) {
	t.Parallel()

	p := storeless(t)
	conformance.RunArtifactStore(t, p.Facts(), p.Artifacts())
}
