package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func runOptionalSets(t *testing.T, suite Suite) {
	t.Helper()

	if suite.New == nil {
		t.Skip("the suite carries no constructor, so there is no root to assert against")
	}

	provider, err := suite.New(context.Background(), suite.Options)
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}

	for _, set := range optionalSets {
		t.Run(set.name, func(t *testing.T) {
			if !set.onRoot(provider) {
				t.Skipf("the provider does not implement %s", set.name)
			}
			for _, port := range wrapPorts(provider) {
				if set.onPort(port.value) {
					t.Errorf("%s is reachable through the wrapped %s port, which only holds while nothing wraps it", set.name, port.name)
				}
			}
			if !set.onRoot(provider) {
				t.Errorf("%s left the root once its ports were wrapped", set.name)
			}
		})
	}
}

var optionalSets = []struct {
	name   string
	onRoot func(providerkit.Provider) bool
	onPort func(any) bool
}{
	{
		name:   "Warmer",
		onRoot: func(p providerkit.Provider) bool { _, ok := p.(providerkit.Warmer); return ok },
		onPort: func(port any) bool { _, ok := port.(providerkit.Warmer); return ok },
	},
	{
		name:   "CodeEmbedder",
		onRoot: func(p providerkit.Provider) bool { _, ok := p.(providerkit.CodeEmbedder); return ok },
		onPort: func(port any) bool { _, ok := port.(providerkit.CodeEmbedder); return ok },
	},
	{
		name:   "StackInspector",
		onRoot: func(p providerkit.Provider) bool { _, ok := p.(providerkit.StackInspector); return ok },
		onPort: func(port any) bool { _, ok := port.(providerkit.StackInspector); return ok },
	},
	{
		name:   "GrantVerifier",
		onRoot: func(p providerkit.Provider) bool { _, ok := p.(providerkit.GrantVerifier); return ok },
		onPort: func(port any) bool { _, ok := port.(providerkit.GrantVerifier); return ok },
	},
}

type namedPort struct {
	name  string
	value any
}

func wrapPorts(p providerkit.Provider) []namedPort {
	return []namedPort{
		{"Bootstrapper", wrappedBootstrapper{p.Bootstrap()}},
		{"Releaser", wrappedReleaser{p.Releases()}},
		{"ArtifactStore", wrappedArtifacts{p.Artifacts()}},
		{"RecordStore", wrappedRecords{p.Records()}},
		{"Sealer", wrappedSealer{p.Sealer()}},
		{"Credentials", wrappedCredentials{p.Credentials()}},
		{"EdgeRegistry", wrappedEdges{p.Edges()}},
		{"DNSRegistry", wrappedDNS{p.DNS()}},
	}
}

type wrappedBootstrapper struct{ providerkit.Bootstrapper }

type wrappedReleaser struct{ providerkit.Releaser }

type wrappedArtifacts struct{ providerkit.ArtifactStore }

type wrappedRecords struct{ providerkit.RecordStore }

type wrappedSealer struct{ providerkit.Sealer }

type wrappedCredentials struct{ providerkit.Credentials }

type wrappedEdges struct{ providerkit.EdgeRegistry }

type wrappedDNS struct{ providerkit.DNSRegistry }

var (
	_ providerkit.Bootstrapper  = wrappedBootstrapper{}
	_ providerkit.Releaser      = wrappedReleaser{}
	_ providerkit.ArtifactStore = wrappedArtifacts{}
	_ providerkit.RecordStore   = wrappedRecords{}
	_ providerkit.Sealer        = wrappedSealer{}
	_ providerkit.Credentials   = wrappedCredentials{}
	_ providerkit.EdgeRegistry  = wrappedEdges{}
	_ providerkit.DNSRegistry   = wrappedDNS{}
)
