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

func (n narrowEdge) Facts() edge.Facts {
	facts := n.Edge.Facts()
	facts.Supported = n.serves
	return facts
}

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

type entitlingEdge struct {
	edge.Edge
	asked *int
	plan  edge.CodeEntitlement
}

func (e entitlingEdge) Hooks() edge.Hooks {
	return edge.Hooks{CheckCodeEntitlement: func(context.Context) (edge.CodeEntitlement, error) {
		*e.asked++
		return e.plan, nil
	}}
}

func TestNeedCheckServesACodeNeedWithoutAskingAnEdgeThatChecksNoEntitlement(t *testing.T) {
	t.Parallel()

	root := servedDescriptor(t, "web", edge.ServeDescriptor{
		Needs: map[edge.Need]edge.NeedDetail{edge.NeedEdgeMiddleware: {Count: 1}},
	})
	front, err := fake.NewEdges(fake.NewRecords()).Open(fake.KindRelay)
	if err != nil {
		t.Fatal(err)
	}
	if front.Hooks().CheckCodeEntitlement != nil {
		t.Fatal("the reference edge checks an entitlement, so it cannot stand for one that checks none")
	}

	records, err := providerkit.NeedCheck{Edge: front, Root: root}.Run(context.Background(), oneApp())
	if err != nil {
		t.Fatalf("Run() over an edge that checks no entitlement = %v, want the code need served", err)
	}
	if !slices.Contains(records["web"].InEffect, edge.NeedEdgeMiddleware) {
		t.Errorf("web's needs in effect are %v, want %s among them", records["web"].InEffect, edge.NeedEdgeMiddleware)
	}
}

func TestNeedCheckRefusesACodeNeedThePlanWithholdsAndAsksOnce(t *testing.T) {
	t.Parallel()

	root := servedDescriptor(t, "web", edge.ServeDescriptor{
		Needs: map[edge.Need]edge.NeedDetail{edge.NeedEdgeMiddleware: {Count: 1}, edge.NeedEdgeRuntime: {Count: 1}},
	})
	front, err := fake.NewEdges(fake.NewRecords()).Open(fake.KindRelay)
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	withheld := entitlingEdge{Edge: front, asked: &asked, plan: edge.CodeEntitlement{Plan: "Workers Free", Granted: edge.EntitlementWithheld}}

	records, err := providerkit.NeedCheck{
		Edge:          withheld,
		Root:          root,
		AllowDegraded: []string{string(edge.NeedEdgeMiddleware), string(edge.NeedEdgeRuntime)},
	}.Run(context.Background(), oneApp())
	if err != nil {
		t.Fatalf("Run() with both code needs waived = %v", err)
	}
	if len(records["web"].InEffect) != 0 {
		t.Errorf("web's needs in effect are %v, want none on a plan that withholds edge code", records["web"].InEffect)
	}
	if asked != 1 {
		t.Errorf("the edge was asked for its entitlement %d times, want once for the whole check", asked)
	}

	var refused *providerkit.EdgeEntitlementError
	_, err = providerkit.NeedCheck{Edge: withheld, Root: root}.Run(context.Background(), oneApp())
	if !errors.As(err, &refused) || refused.Plan != "Workers Free" {
		t.Errorf("Run() with nothing waived = %v, want the plan named in an entitlement refusal", err)
	}
}
