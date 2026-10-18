package conformance

import (
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

const Root records.Root = "conformance"

func RunPorts(t *testing.T, p provider.Provider, vendorRoots ...records.Root) {
	t.Helper()

	t.Run("Store", func(t *testing.T) { RunStore(t, p.KeyValues()) })
	t.Run("Cipher", func(t *testing.T) { RunCipher(t, p.Cipher()) })
	facts := p.Facts()
	t.Run("ArtifactStore", func(t *testing.T) { RunArtifactStore(t, facts, p.Artifacts()) })
	t.Run("Stacks", func(t *testing.T) {
		RunStacks(t, facts, p.Stacks(), p.Artifacts(), p.KeyValues(), vendorRoots...)
	})
	t.Run("Bootstrap", func(t *testing.T) {
		RunBootstrap(t, bootstrapOf(t, p), facts.DefaultEdge)
	})
	t.Run("Credentials", func(t *testing.T) { RunCredentials(t, p.Credentials()) })
	t.Run("Edges", func(t *testing.T) { RunEdges(t, facts, p.Edges()) })
	t.Run("Routers", func(t *testing.T) { RunRouters(t, facts, p.Edges(), p.Routers()) })
	t.Run("DNS", func(t *testing.T) { RunDNS(t, facts, p.DNS()) })
	t.Run("Workers", func(t *testing.T) { RunWorkers(t, facts) })
	t.Run("FunctionBuild", func(t *testing.T) { RunFunctionBuild(t, facts) })
	t.Run("KVStores", func(t *testing.T) { RunKVStores(t, facts) })
	t.Run("Realtime", func(t *testing.T) { RunRealtime(t, facts) })
	t.Run("Logs", func(t *testing.T) { RunLogs(t, facts, p.Logs()) })
}

func bootstrapOf(t *testing.T, p provider.Provider) provider.Bootstrap {
	t.Helper()
	bootstrap, err := p.Bootstrap(p.Facts().DefaultEdge)
	if err != nil {
		t.Fatalf("Bootstrap(%q) error = %v, want the bootstrap for this provider's default edge", p.Facts().DefaultEdge, err)
	}
	return bootstrap
}

func conformanceIn(tier environment.Tier, t *testing.T) records.Partition {
	return records.Partition{Tier: tier, Root: Root, Path: []string{t.Name()}}
}

func under(t *testing.T, path ...string) records.Key {
	return conformanceIn(environment.TierProduction, t).Key(path...)
}

func text(value string) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
