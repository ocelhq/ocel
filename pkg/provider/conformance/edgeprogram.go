package conformance

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type EdgeProgramFunc func(ctx context.Context, req provider.EdgeProgramRequest) (provider.EdgeProgram, error)

const edgeProgramPreviewKey edge.PreviewKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

var edgeProgramGate = edge.PreviewGate{
	Key:           "gate-key",
	PasswordMAC:   "password-mac",
	BypassSecrets: []string{"bypass-one"},
	AllowOptions:  []string{"/api/"},
}

func RunEdgeProgram(t *testing.T, program EdgeProgramFunc) {
	t.Helper()

	projectPreview := provider.EdgeProgramRequest{
		Tier:              environment.TierPreview,
		Slug:              "shop",
		Env:               "pr-7",
		PreviewBaseDomain: "preview.shop.example",
		PreviewKey:        edgeProgramPreviewKey,
	}
	sharedEntry := provider.EdgeProgramRequest{
		Tier:              environment.TierPreview,
		PreviewBaseDomain: "preview.acme.com",
		PreviewKey:        edgeProgramPreviewKey,
	}

	t.Run("a project's preview program carries its gate keys as a worker secret", func(t *testing.T) {
		req := projectPreview
		gate := edgeProgramGate
		req.PreviewGate = &gate
		worker := buildEdgeWorker(t, program, req)
		var got edge.PreviewGate
		readEncodedSecret(t, worker, edge.PreviewGateVar, &got)
		if !reflect.DeepEqual(got, edgeProgramGate) {
			t.Errorf("Secrets[%s] = %+v, want the gate the request carried, %+v", edge.PreviewGateVar, got, edgeProgramGate)
		}
	})

	t.Run("the shared preview entry's program carries every project's gate keys as a worker secret", func(t *testing.T) {
		req := sharedEntry
		req.PreviewGates = map[string]edge.PreviewGate{"shop": edgeProgramGate}
		worker := buildEdgeWorker(t, program, req)
		var got map[string]edge.PreviewGate
		readEncodedSecret(t, worker, edge.PreviewGatesVar, &got)
		if !reflect.DeepEqual(got, req.PreviewGates) {
			t.Errorf("Secrets[%s] = %+v, want the gates the request carried, %+v", edge.PreviewGatesVar, got, req.PreviewGates)
		}
	})

	t.Run("a program handed no gate keys carries no gate secret", func(t *testing.T) {
		for name, req := range map[string]provider.EdgeProgramRequest{
			"a project's preview":      projectPreview,
			"the shared preview entry": sharedEntry,
		} {
			worker := buildEdgeWorker(t, program, req)
			for _, secret := range []string{edge.PreviewGateVar, edge.PreviewGatesVar} {
				if _, carried := worker.Secrets[secret]; carried {
					t.Errorf("%s carries Secrets[%s], want none: the request carried no gate keys", name, secret)
				}
			}
		}
	})
}

func buildEdgeWorker(t *testing.T, program EdgeProgramFunc, req provider.EdgeProgramRequest) edge.Worker {
	t.Helper()
	built, err := program(t.Context(), req)
	if err != nil {
		t.Fatalf("ProgramEdge(%+v) error = %v", req, err)
	}
	if built.Spec == nil {
		t.Fatalf("ProgramEdge(%+v) built no program", req)
	}
	return built.Spec.Worker
}

func readEncodedSecret(t *testing.T, worker edge.Worker, name string, into any) {
	t.Helper()
	if _, plain := worker.Variables[name]; plain {
		t.Errorf("Variables carries %s, want it only as a secret: it holds the keys that admit a visitor", name)
	}
	encoded, carried := worker.Secrets[name]
	if !carried {
		t.Fatalf("Secrets has no %s, want the gate keys the request carried", name)
	}
	if err := json.Unmarshal([]byte(encoded), into); err != nil {
		t.Fatalf("Secrets[%s] = %q, want the gate keys as JSON: %v", name, encoded, err)
	}
}
