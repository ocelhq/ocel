package providerkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func servedDescriptor(t *testing.T, app string, desc edge.ServeDescriptor) string {
	t.Helper()
	root := t.TempDir()
	dir := providerkit.AppArtifactRoot(root, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, edge.ServeDescriptorFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func oneApp() *contractv1.Manifest {
	return &contractv1.Manifest{Slug: "shop", Apps: []*contractv1.ManifestApp{{Name: "web"}}}
}

type narrowEdge struct {
	edge.Edge
	serves []edge.Need
}

func (n narrowEdge) Supported() []edge.Need { return n.serves }

func TestNeedCheckRecordsWhatTheEdgeServes(t *testing.T) {
	t.Parallel()

	root := servedDescriptor(t, "web", edge.ServeDescriptor{
		Needs: map[edge.Need]edge.NeedDetail{edge.NeedStreaming: {Count: 3}},
	})
	front, err := fake.NewEdges(fake.NewRecords()).Open(fake.KindRelay)
	if err != nil {
		t.Fatal(err)
	}

	records, err := providerkit.NeedCheck{Edge: front, Root: root}.Run(context.Background(), oneApp())
	if err != nil {
		t.Fatalf("Run() over an edge that serves every need = %v", err)
	}
	if !slices.Contains(records["web"].InEffect, edge.NeedStreaming) {
		t.Fatalf("web's needs in effect are %v, want %s among them", records["web"].InEffect, edge.NeedStreaming)
	}
	if len(records["web"].Waived) != 0 {
		t.Errorf("web waived %v against an edge that serves it", records["web"].Waived)
	}
}

func TestNeedCheckRefusesANeedTheEdgeDoesNotServe(t *testing.T) {
	t.Parallel()

	root := servedDescriptor(t, "web", edge.ServeDescriptor{
		Needs: map[edge.Need]edge.NeedDetail{edge.NeedStreaming: {Routes: []string{"/feed"}}},
	})
	front, err := fake.NewEdges(fake.NewRecords()).Open(fake.KindRelay)
	if err != nil {
		t.Fatal(err)
	}

	_, err = providerkit.NeedCheck{Edge: narrowEdge{Edge: front}, Root: root}.Run(context.Background(), oneApp())
	var unsupported *providerkit.UnsupportedNeedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Run() against an edge serving nothing = %v, want an UnsupportedNeedError", err)
	}
	if !strings.Contains(unsupported.Error(), "/feed") {
		t.Errorf("the refusal reads %q, want it to name the routes it affects", unsupported)
	}
}

func TestNeedCheckDegradesAWaivedNeedRatherThanRefusing(t *testing.T) {
	t.Parallel()

	root := servedDescriptor(t, "web", edge.ServeDescriptor{
		Needs: map[edge.Need]edge.NeedDetail{edge.NeedStreaming: {Count: 1}},
	})
	front, err := fake.NewEdges(fake.NewRecords()).Open(fake.KindRelay)
	if err != nil {
		t.Fatal(err)
	}

	var degraded []edge.Need
	records, err := providerkit.NeedCheck{
		Edge:          narrowEdge{Edge: front},
		Root:          root,
		AllowDegraded: []string{string(edge.NeedStreaming)},
		Degraded:      func(need edge.Need, _ string) { degraded = append(degraded, need) },
	}.Run(context.Background(), oneApp())
	if err != nil {
		t.Fatalf("Run() with the need waived = %v, want it deployed degraded", err)
	}
	if !slices.Contains(records["web"].Waived, edge.NeedStreaming) {
		t.Errorf("web's waived needs are %v, want %s among them", records["web"].Waived, edge.NeedStreaming)
	}
	if !slices.Contains(degraded, edge.NeedStreaming) {
		t.Errorf("the deploy reported %v as degraded, want %s said out loud", degraded, edge.NeedStreaming)
	}
}

func TestNeedCheckRefusesANeedNoEdgeKnows(t *testing.T) {
	t.Parallel()

	root := servedDescriptor(t, "web", edge.ServeDescriptor{
		Needs: map[edge.Need]edge.NeedDetail{"teleportation": {Count: 1}},
	})
	front, err := fake.NewEdges(fake.NewRecords()).Open(fake.KindRelay)
	if err != nil {
		t.Fatal(err)
	}

	_, err = providerkit.NeedCheck{Edge: front, Root: root}.Run(context.Background(), oneApp())
	var unknown *providerkit.UnknownNeedError
	if !errors.As(err, &unknown) {
		t.Fatalf("Run() over a need no edge knows = %v, want an UnknownNeedError", err)
	}
}

func TestNeedCheckPassesAnAppThatShipsNoDescriptor(t *testing.T) {
	t.Parallel()

	front, err := fake.NewEdges(fake.NewRecords()).Open(fake.KindRelay)
	if err != nil {
		t.Fatal(err)
	}

	records, err := providerkit.NeedCheck{Edge: front, Root: t.TempDir()}.Run(context.Background(), oneApp())
	if err != nil {
		t.Fatalf("Run() over an app with no serve descriptor = %v, want it to pass", err)
	}
	if len(records) != 0 {
		t.Errorf("Run() recorded %v for an app that declares nothing", records)
	}
}
