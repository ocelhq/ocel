package edge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
)

func TestThePreviewGateAnswersEveryConformanceVector(t *testing.T) {
	t.Parallel()

	edgeconformance.RunPreviewGate(t)
}

func TestAPreviewPasswordHashesToTheValueTheVectorsStore(t *testing.T) {
	t.Parallel()

	const want = "f18e0cd484c475051052b9fed4f125e1db487c694fc74b1128b40bb4af41e5a3"
	if got := edge.HashPreviewPassword("test-preview-key", "JBSWY3DPEHPK3PXP2345"); got != want {
		t.Errorf("HashPreviewPassword = %q, want %q", got, want)
	}
}

func TestAPreviewGateEncodesAsTheJSONTheEdgeContractFixtureRecords(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "edge", "contract", "fixtures", "preview-gate.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Gates []struct {
			Name    string           `json:"name"`
			Gate    edge.PreviewGate `json:"gate"`
			Encoded string           `json:"encoded"`
		} `json:"gates"`
		SharedEntry struct {
			Slug    string `json:"slug"`
			Encoded string `json:"encoded"`
		} `json:"sharedEntry"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	unlisted := func(gate edge.PreviewGate) edge.PreviewGate {
		if len(gate.BypassSecrets) == 0 {
			gate.BypassSecrets = nil
		}
		if len(gate.AllowOptions) == 0 {
			gate.AllowOptions = nil
		}
		return gate
	}
	for _, example := range fixture.Gates {
		t.Run(example.Name, func(t *testing.T) {
			encoded, err := json.Marshal(unlisted(example.Gate))
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != example.Encoded {
				t.Errorf("json.Marshal = %s, want %s", encoded, example.Encoded)
			}
		})
	}
	encoded, err := json.Marshal(map[string]edge.PreviewGate{fixture.SharedEntry.Slug: unlisted(fixture.Gates[0].Gate)})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != fixture.SharedEntry.Encoded {
		t.Errorf("json.Marshal of the shared entry's gates = %s, want %s", encoded, fixture.SharedEntry.Encoded)
	}
}
