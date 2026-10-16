package bootstrap

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const fakeLayerAccount = "arn:aws:lambda:us-east-1:111122223333:layer:"

type fakeLayers struct {
	mu        sync.Mutex
	versions  map[string][]int64
	next      map[string]int64
	published []*lambda.PublishLayerVersionInput
	deleted   []string
	lists     int
	staleFor  int

	beforePublish func(name string)
}

func newFakeLayers() *fakeLayers {
	return &fakeLayers{versions: map[string][]int64{}, next: map[string]int64{}}
}

func layerVersionARN(name string, version int64) string {
	return fmt.Sprintf("%s%s:%d", fakeLayerAccount, name, version)
}

func (f *fakeLayers) seed(name string, version int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[name] = append(f.versions[name], version)
	f.next[name] = max(f.next[name], version)
}

func (f *fakeLayers) resolves(arn string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, versions := range f.versions {
		for _, version := range versions {
			if layerVersionARN(name, version) == arn {
				return true
			}
		}
	}
	return false
}

func (f *fakeLayers) ListLayerVersions(_ context.Context, in *lambda.ListLayerVersionsInput, _ ...func(*lambda.Options)) (*lambda.ListLayerVersionsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.lists <= f.staleFor {
		return &lambda.ListLayerVersionsOutput{}, nil
	}
	name := aws.ToString(in.LayerName)
	versions := slices.Clone(f.versions[name])
	slices.Sort(versions)
	slices.Reverse(versions)
	out := &lambda.ListLayerVersionsOutput{}
	for _, version := range versions {
		out.LayerVersions = append(out.LayerVersions, lambdatypes.LayerVersionsListItem{
			LayerVersionArn: aws.String(layerVersionARN(name, version)),
			Version:         version,
		})
	}
	return out, nil
}

func (f *fakeLayers) PublishLayerVersion(_ context.Context, in *lambda.PublishLayerVersionInput, _ ...func(*lambda.Options)) (*lambda.PublishLayerVersionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.LayerName)
	if f.beforePublish != nil {
		f.beforePublish(name)
	}
	f.next[name]++
	version := f.next[name]
	f.versions[name] = append(f.versions[name], version)
	f.published = append(f.published, in)
	return &lambda.PublishLayerVersionOutput{
		LayerVersionArn: aws.String(layerVersionARN(name, version)),
		Version:         version,
	}, nil
}

func (f *fakeLayers) DeleteLayerVersion(_ context.Context, in *lambda.DeleteLayerVersionInput, _ ...func(*lambda.Options)) (*lambda.DeleteLayerVersionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.LayerName)
	f.versions[name] = slices.DeleteFunc(f.versions[name], func(v int64) bool { return v == aws.ToInt64(in.VersionNumber) })
	f.deleted = append(f.deleted, layerVersionARN(name, aws.ToInt64(in.VersionNumber)))
	return &lambda.DeleteLayerVersionOutput{}, nil
}

func shippedRuntime(label string) map[string]payloads.Payload {
	shipped := map[string]payloads.Payload{}
	for _, architecture := range runtimeArches() {
		shipped[architecture] = payloads.Of([]byte(label + " runtime for " + architecture))
	}
	return shipped
}

const fixtureStateTable = "ocel-state-table"

type fakeRecords struct {
	mu     sync.Mutex
	rows   map[string]keyvalue.Entry
	rev    int
	writes int
	tables []string
}

func newFakeRecords() *fakeRecords {
	return &fakeRecords{rows: map[string]keyvalue.Entry{}}
}

func (f *fakeRecords) in(table string) keyvalue.Store {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tables = append(f.tables, table)
	return f
}

func (f *fakeRecords) Read(_ context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	recorded, ok := f.rows[key.String()]
	if !ok {
		return keyvalue.Entry{}, keyvalue.ErrNotFound
	}
	return recorded, nil
}

func (f *fakeRecords) Write(_ context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := keyvalue.RefuseUnwritable(entry); err != nil {
		return "", err
	}
	f.writes++
	if f.rows[entry.Key.String()].Revision != entry.Revision {
		return "", keyvalue.ErrStale
	}
	f.rev++
	entry.Revision = keyvalue.Revision(fmt.Sprint(f.rev))
	f.rows[entry.Key.String()] = entry
	return entry.Revision, nil
}

func (f *fakeRecords) WritePair(context.Context, keyvalue.Entry, keyvalue.Entry) error {
	return fmt.Errorf("fakeRecords: no runtime layer write spans two entries")
}

func (f *fakeRecords) Remove(_ context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	recorded, ok := f.rows[key.String()]
	if !ok {
		return keyvalue.ErrNotFound
	}
	if recorded.Revision != expected {
		return keyvalue.ErrStale
	}
	delete(f.rows, key.String())
	return nil
}

func (f *fakeRecords) List(context.Context, keyvalue.Partition, ...string) ([]keyvalue.Entry, error) {
	return nil, fmt.Errorf("fakeRecords: the runtime layers are reached by key, never listed")
}

func (f *fakeRecords) recorded(t *testing.T, tier environment.Tier) []string {
	t.Helper()
	names, err := readRecordedRuntimeLayers(context.Background(), f, tier)
	if err != nil {
		t.Fatalf("readRecordedRuntimeLayers: %v", err)
	}
	return names
}

func (f *fakeRecords) record(t *testing.T, tier environment.Tier, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := recordRuntimeLayer(context.Background(), f, tier, name); err != nil {
			t.Fatalf("recordRuntimeLayer: %v", err)
		}
	}
}

func layeringAPIs(layers *fakeLayers, store ObjectStore, records *fakeRecords) APIs {
	return APIs{Layers: layers, Store: store, Records: records.in}
}

func shippedLayerNames(shipped map[string]payloads.Payload) []string {
	names := make([]string, 0, len(shipped))
	for _, architecture := range runtimeArches() {
		names = append(names, runtimeLayerName(defaultNamespace, environment.TierProduction, architecture, shipped[architecture].SHA256))
	}
	slices.Sort(names)
	return names
}

func ensureBuild(t *testing.T, apis APIs, shipped map[string]payloads.Payload, log *repairLog) map[string]string {
	t.Helper()
	req := RuntimeLayerRequest{ArtifactBucket: fixtureBucket, StateTable: fixtureStateTable}
	got, err := ensureRuntimeLayers(context.Background(), apis, defaultNamespace, environment.TierProduction, req, shipped, log)
	if err != nil {
		t.Fatalf("ensureRuntimeLayers: %v", err)
	}
	return got
}

func TestEnsureRuntimeLayers(t *testing.T) {
	t.Run("a deploy from another Ocel version keeps the runtime layer the first version's releases boot through", func(t *testing.T) {
		layers, store := newFakeLayers(), newFakeObjectStore()
		apis := layeringAPIs(layers, store, newFakeRecords())
		var log repairLog

		first := ensureBuild(t, apis, shippedRuntime("ocel v1"), &log)
		second := ensureBuild(t, apis, shippedRuntime("ocel v2"), &log)

		if len(layers.deleted) != 0 {
			t.Errorf("deleted layer versions %v, and a release deployed from the first Ocel version still boots through them", layers.deleted)
		}
		for _, architecture := range runtimeArches() {
			if first[architecture] == "" || second[architecture] == "" {
				t.Fatalf("%s runtime: the first Ocel version got %q, the second %q, want an ARN for each", architecture, first[architecture], second[architecture])
			}
			if first[architecture] == second[architecture] {
				t.Errorf("both Ocel versions boot their %s functions through %s, want a layer per runtime", architecture, first[architecture])
			}
			if !layers.resolves(first[architecture]) {
				t.Errorf("the first Ocel version's %s runtime %s no longer resolves after the second deployed", architecture, first[architecture])
			}
		}
		reread, err := readRuntimeLayers(context.Background(), layers, defaultNamespace, environment.TierProduction, shippedRuntime("ocel v1"))
		if err != nil {
			t.Fatalf("readRuntimeLayers: %v", err)
		}
		if !maps.Equal(reread, first) {
			t.Errorf("the first Ocel version reads its runtime back as %v, want %v", reread, first)
		}
	})

	t.Run("two deploys publishing the same runtime at once each get a version of their own that resolves", func(t *testing.T) {
		layers, store := newFakeLayers(), newFakeObjectStore()
		layers.staleFor = 2 * len(runtimeArches())
		apis := layeringAPIs(layers, store, newFakeRecords())
		shipped := shippedRuntime("ocel v1")
		var log repairLog

		one := ensureBuild(t, apis, shipped, &log)
		other := ensureBuild(t, apis, shipped, &log)

		for _, architecture := range runtimeArches() {
			if one[architecture] == other[architecture] {
				t.Errorf("both racing deploys boot their %s functions through %q, want each its own published version", architecture, one[architecture])
			}
			for _, arn := range []string{one[architecture], other[architecture]} {
				if !layers.resolves(arn) {
					t.Errorf("a deploy racing another for the %s runtime boots through %q, which does not resolve", architecture, arn)
				}
			}
		}
		if len(layers.deleted) != 0 {
			t.Errorf("deleted layer versions %v while two deploys published the same runtime", layers.deleted)
		}
	})

	t.Run("a runtime already published is neither published nor recorded again", func(t *testing.T) {
		layers := newFakeLayers()
		shipped := shippedRuntime("ocel v1")
		for _, name := range shippedLayerNames(shipped) {
			layers.seed(name, 1)
			layers.seed(name, 3)
		}
		store, records := newFakeObjectStore(), newFakeRecords()
		var log repairLog

		got := ensureBuild(t, layeringAPIs(layers, store, records), shipped, &log)

		if len(layers.published) != 0 {
			t.Errorf("published %d layer versions of a runtime the account already has", len(layers.published))
		}
		if store.puts != 0 {
			t.Errorf("placed %d payloads for a runtime the account already has", store.puts)
		}
		if records.writes != 0 {
			t.Errorf("wrote the runtime layer record %d times for a runtime the account already has", records.writes)
		}
		for _, architecture := range runtimeArches() {
			want := layerVersionARN(runtimeLayerName(defaultNamespace, environment.TierProduction, architecture, shipped[architecture].SHA256), 3)
			if got[architecture] != want {
				t.Errorf("the %s runtime is %q, want the latest version %q", architecture, got[architecture], want)
			}
		}
	})

	t.Run("a first publish records the layer in the tier's state table before it publishes it", func(t *testing.T) {
		layers, store, records := newFakeLayers(), newFakeObjectStore(), newFakeRecords()
		layers.beforePublish = func(name string) {
			if !slices.Contains(records.recorded(t, environment.TierProduction), name) {
				t.Errorf("published %s before recording it, and a destroy finds only the layers it recorded", name)
			}
		}
		shipped := shippedRuntime("ocel v1")
		var log repairLog

		ensureBuild(t, layeringAPIs(layers, store, records), shipped, &log)
		ensureBuild(t, layeringAPIs(layers, store, records), shippedRuntime("ocel v2"), &log)

		want := slices.Concat(shippedLayerNames(shipped), shippedLayerNames(shippedRuntime("ocel v2")))
		slices.Sort(want)
		if got := records.recorded(t, environment.TierProduction); !slices.Equal(got, want) {
			t.Errorf("the tier records runtime layers %v, want %v", got, want)
		}
		for _, table := range records.tables {
			if table != fixtureStateTable {
				t.Errorf("recorded the runtime layers in table %q, want the tier's state table %q", table, fixtureStateTable)
			}
		}
	})

	t.Run("a first publish places the payload and publishes it for its architecture", func(t *testing.T) {
		layers, store := newFakeLayers(), newFakeObjectStore()
		shipped := shippedRuntime("ocel v1")
		var log repairLog

		ensureBuild(t, layeringAPIs(layers, store, newFakeRecords()), shipped, &log)

		if len(layers.published) != len(runtimeArches()) {
			t.Fatalf("published %d layer versions, want one per architecture", len(layers.published))
		}
		for _, in := range layers.published {
			architecture := arch.X8664
			if strings.Contains(aws.ToString(in.LayerName), runtimeArchTokens[arch.ARM64]) {
				architecture = arch.ARM64
			}
			key := payloads.Key(runtimeLayerKeyPrefix, shipped[architecture].SHA256)
			if !store.has(key) {
				t.Errorf("the %s runtime was published before its payload was placed at %s", architecture, key)
			}
			if aws.ToString(in.Content.S3Bucket) != fixtureBucket || aws.ToString(in.Content.S3Key) != key {
				t.Errorf("the %s layer is published from s3://%s/%s, want s3://%s/%s", architecture, aws.ToString(in.Content.S3Bucket), aws.ToString(in.Content.S3Key), fixtureBucket, key)
			}
			if !slices.Equal(in.CompatibleArchitectures, []lambdatypes.Architecture{lambdatypes.Architecture(architecture)}) {
				t.Errorf("the %s layer is compatible with %v, want only %s", architecture, in.CompatibleArchitectures, architecture)
			}
		}
		if !log.says("INFO Published the ") {
			t.Errorf("the deploy said %v, want it to say it published the runtime", log.Lines())
		}
	})
}

func seedTiersRuntimeLayers(t *testing.T, layers *fakeLayers, records *fakeRecords) (own, kept []string) {
	t.Helper()
	own = []string{
		runtimeLayerName(defaultNamespace, environment.TierProduction, arch.X8664, strings.Repeat("a", 64)),
		runtimeLayerName(defaultNamespace, environment.TierProduction, arch.ARM64, strings.Repeat("b", 64)),
	}
	preview := runtimeLayerName(defaultNamespace, environment.TierPreview, arch.X8664, strings.Repeat("a", 64))
	kept = []string{
		preview,
		runtimeLayerName(Namespace("other"), environment.TierProduction, arch.X8664, strings.Repeat("a", 64)),
		"customer-layer",
	}
	for _, name := range append(slices.Clone(own), kept...) {
		layers.seed(name, 1)
		layers.seed(name, 2)
	}
	records.record(t, environment.TierProduction, own...)
	records.record(t, environment.TierPreview, preview)
	return own, kept
}

func TestTeardownDeletesOnlyTheRuntimeLayersItsTierRecorded(t *testing.T) {
	apis, _, _, _ := teardownFakes(t)
	layers, records := newFakeLayers(), newFakeRecords()
	apis.Layers, apis.Records = layers, records.in
	own, kept := seedTiersRuntimeLayers(t, layers, records)

	if err := Teardown(context.Background(), apis, defaultNamespace, environment.TierProduction, nil); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	for _, name := range own {
		for _, version := range []int64{1, 2} {
			if layers.resolves(layerVersionARN(name, version)) {
				t.Errorf("a destroy of the production tier left its runtime layer version %s:%d behind", name, version)
			}
		}
	}
	for _, name := range kept {
		if !layers.resolves(layerVersionARN(name, 2)) {
			t.Errorf("a destroy of the production tier deleted %s, a layer it never published", name)
		}
	}
	if left := records.recorded(t, environment.TierProduction); len(left) != 0 {
		t.Errorf("a destroy of the production tier left its runtime layer record naming %v", left)
	}
	if got := records.recorded(t, environment.TierPreview); len(got) != 1 {
		t.Errorf("a destroy of the production tier changed the preview tier's runtime layer record to %v", got)
	}
}

func TestADestroyPlanListsTheRuntimeLayerVersionsItsTierRecorded(t *testing.T) {
	layers, records := newFakeLayers(), newFakeRecords()
	own, _ := seedTiersRuntimeLayers(t, layers, records)

	group, found := removeRuntimeLayers(context.Background(), layers, records.in(fixtureStateTable), defaultNamespace, environment.TierProduction)

	if !found {
		t.Fatal("the destroy plan shows no runtime layers, and the tier recorded two")
	}
	var want []string
	for _, name := range own {
		want = append(want, name+":1", name+":2")
	}
	var got []string
	for _, change := range group.Changes {
		got = append(got, change.Name)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the destroy plan deletes %v, want %v", got, want)
	}
}

func TestABootstrapPlanShowsTheRuntimeLayersItWouldPublish(t *testing.T) {
	layers := newFakeLayers()
	shipped, err := shippedRuntimeLayers()
	if err != nil {
		t.Fatalf("shippedRuntimeLayers: %v", err)
	}
	published := runtimeLayerName(defaultNamespace, environment.TierProduction, arch.X8664, shipped[arch.X8664].SHA256)
	layers.seed(published, 1)

	group := planRuntimeLayers(context.Background(), layers, Reading{ns: defaultNamespace, tier: environment.TierProduction})

	want := map[string]provider.ChangeAction{
		published: provider.ActionKeep,
		runtimeLayerName(defaultNamespace, environment.TierProduction, arch.ARM64, shipped[arch.ARM64].SHA256): provider.ActionCreate,
	}
	got := map[string]provider.ChangeAction{}
	for _, change := range group.Changes {
		got[change.Name] = change.Action
	}
	if !maps.Equal(got, want) {
		t.Errorf("the plan shows %v, want %v", got, want)
	}
	if group.Action != provider.ActionUpdate {
		t.Errorf("the plan's runtime group is %q, want %q while one architecture is still to publish", group.Action, provider.ActionUpdate)
	}
}

func TestADryPlanBootsThroughTheRuntimeLayerTheDeployWouldPublish(t *testing.T) {
	layers := newFakeLayers()
	shipped := shippedRuntime("ocel v1")
	published := runtimeLayerName(defaultNamespace, environment.TierProduction, arch.X8664, shipped[arch.X8664].SHA256)
	layers.seed(published, 2)

	got, err := readRuntimeLayersToPlan(context.Background(), layers, defaultNamespace, environment.TierProduction, shipped)
	if err != nil {
		t.Fatalf("readRuntimeLayersToPlan: %v", err)
	}

	want := map[string]string{
		arch.X8664: layerVersionARN(published, 2),
		arch.ARM64: awsports.RuntimeLayerToPublish,
	}
	if !maps.Equal(got, want) {
		t.Errorf("a dry plan boots through %v, want %v: the published runtime, and the one the deploy would publish", got, want)
	}
	if len(layers.published) != 0 {
		t.Errorf("a dry plan published %d layer versions", len(layers.published))
	}
}
