package providerserver

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var shopPreviewEdge = stackrecords.EdgeStackKey(environment.TierPreview, "shop")

func recordEdgeState(t *testing.T, store keyvalue.Store, state stackrecords.EdgeState) {
	t.Helper()
	entry, err := keyvalue.ReadOrEmpty(context.Background(), store, shopPreviewEdge)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Value, err = json.Marshal(state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
}

func readEdgeHosts(t *testing.T, store keyvalue.Store) []string {
	t.Helper()
	state, err := (&edgeStateStore{keyValues: store, name: shopPreviewEdge}).read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(state.Hosts))
}

func previewHost(pointer string) stackrecords.HostnameState {
	return stackrecords.HostnameState{Edge: edge.Kind("fake"), Pointer: pointer}
}

func TestAnEdgeCheckpointKeepsAHostAnotherPreviewRecordedAfterThisOneRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	recordEdgeState(t, store, stackrecords.EdgeState{Edge: edge.StackState{Slug: "shop"}, Hosts: map[string]stackrecords.HostnameState{"a.preview.test": previewHost("pr-1")}})

	mine := &edgeStateStore{keyValues: store, name: shopPreviewEdge}
	state, err := mine.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	theirs := &edgeStateStore{keyValues: store, name: shopPreviewEdge}
	other, err := theirs.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other.SetHost("b.preview.test", previewHost("pr-2"))
	if err := theirs.write(ctx, other); err != nil {
		t.Fatal(err)
	}

	state.SetHost("c.preview.test", previewHost("pr-3"))
	if err := mine.write(ctx, state); err != nil {
		t.Fatalf("write() after another preview's write = %v, want this preview's host merged onto it", err)
	}
	if got, want := readEdgeHosts(t, store), []string{"a.preview.test", "b.preview.test", "c.preview.test"}; !slices.Equal(got, want) {
		t.Errorf("recorded hosts %v, want %v: neither preview's host may overwrite the other's", got, want)
	}
}

func TestAnEdgeCheckpointRacedBetweenItsReadAndItsWriteReappliesItsChangeOntoTheNewerRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	recordEdgeState(t, store, stackrecords.EdgeState{Edge: edge.StackState{Slug: "shop"}, Hosts: map[string]stackrecords.HostnameState{"a.preview.test": previewHost("pr-1")}})

	mine := &edgeStateStore{keyValues: store, name: shopPreviewEdge}
	state, err := mine.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.BeforeNextWrite(shopPreviewEdge, func() {
		theirs := &edgeStateStore{keyValues: store, name: shopPreviewEdge}
		other, err := theirs.read(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		other.SetHost("b.preview.test", previewHost("pr-2"))
		if err := theirs.write(ctx, other); err != nil {
			t.Error(err)
		}
	})

	state.Forget("a.preview.test")
	state.SetHost("c.preview.test", previewHost("pr-3"))
	if err := mine.write(ctx, state); err != nil {
		t.Fatalf("write() raced by another preview = %v, want it re-read and re-applied", err)
	}
	if got, want := readEdgeHosts(t, store), []string{"b.preview.test", "c.preview.test"}; !slices.Equal(got, want) {
		t.Errorf("recorded hosts %v, want %v: this preview's forget and add on top of the other's add", got, want)
	}
}

func TestAnEdgeCheckpointLeavesTheEdgeStackAnotherPreviewWroteWhenThisOneLeftItAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	recordEdgeState(t, store, stackrecords.EdgeState{Edge: edge.StackState{Slug: "shop", Bound: []string{"*.one.test"}}})

	mine := &edgeStateStore{keyValues: store, name: shopPreviewEdge}
	state, err := mine.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	theirs := &edgeStateStore{keyValues: store, name: shopPreviewEdge}
	other, err := theirs.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other.Edge.Bound = []string{"*.one.test", "*.two.test"}
	if err := theirs.write(ctx, other); err != nil {
		t.Fatal(err)
	}

	state.SetHost("c.preview.test", previewHost("pr-3"))
	if err := mine.write(ctx, state); err != nil {
		t.Fatal(err)
	}
	recorded, err := (&edgeStateStore{keyValues: store, name: shopPreviewEdge}).read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"*.one.test", "*.two.test"}; !slices.Equal(recorded.Edge.Bound, want) {
		t.Errorf("recorded edge binds %v, want %v: this preview never changed the edge stack", recorded.Edge.Bound, want)
	}
}

func TestRememberingAProjectRacedByAnotherPreviewKeepsTheFeatureThatOneAdded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vendor := fake.NewProvider(fake.Options{})
	store := vendor.KeyValues().(*fake.KeyValues)
	name := stackrecords.ProjectKey(environment.TierPreview, "shop")
	writeProject(t, store, name, []string{"edge"})

	store.BeforeNextWrite(name, func() { writeProject(t, store, name, []string{"edge", "images"}) })
	run := &deployRun{edgeSession: &edgeSession{provider: vendor}, spec: provider.DeploySpec{Tier: environment.TierPreview, Slug: "shop"}, features: []string{"edge", "queues"}}
	if err := run.rememberProject(ctx); err != nil {
		t.Fatalf("rememberProject() raced by another preview = %v, want it re-read and re-applied", err)
	}
	if got, want := readProjectFeatures(t, store, name), []string{"edge", "queues", "images"}; !slices.Equal(got, want) {
		t.Errorf("recorded features %v, want %v: this preview's features and the one the other preview added", got, want)
	}
}

func TestEnsuringAProjectAnotherPreviewRecordedFirstKeepsThatRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vendor := fake.NewProvider(fake.Options{})
	store := vendor.KeyValues().(*fake.KeyValues)
	name := stackrecords.ProjectKey(environment.TierPreview, "shop")

	store.BeforeNextWrite(name, func() { writeProject(t, store, name, []string{"images"}) })
	run := &deployRun{edgeSession: &edgeSession{provider: vendor}, spec: provider.DeploySpec{Tier: environment.TierPreview, Slug: "shop"}, features: []string{"queues"}}
	if err := run.ensureProject(ctx); err != nil {
		t.Fatalf("ensureProject() raced by another preview = %v, want the record that preview wrote kept", err)
	}
	if got, want := readProjectFeatures(t, store, name), []string{"images"}; !slices.Equal(got, want) {
		t.Errorf("recorded features %v, want %v", got, want)
	}
}

func writeProject(t *testing.T, store keyvalue.Store, name keyvalue.Key, features []string) {
	t.Helper()
	entry, err := keyvalue.ReadOrEmpty(context.Background(), store, name)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Value, err = json.Marshal(stackrecords.Project{Features: features}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
}

func readProjectFeatures(t *testing.T, store keyvalue.Store, name keyvalue.Key) []string {
	t.Helper()
	entry, err := store.Read(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	var project stackrecords.Project
	if err := json.Unmarshal(entry.Value, &project); err != nil {
		t.Fatal(err)
	}
	return project.Features
}
