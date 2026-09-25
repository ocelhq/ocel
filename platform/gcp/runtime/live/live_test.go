package live

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
	vars "github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type stores struct {
	records providerkit.RecordStore
	sealer  providerkit.Cipher
}

func fakeStores() stores {
	return stores{records: fake.NewRecords(), sealer: fake.NewCipher()}
}

func (s stores) store() values.Store {
	return values.Store{Records: s.records, Cipher: s.sealer}
}

func (s stores) set(t *testing.T, scope values.Scope, at values.Coordinate, plaintext string) {
	t.Helper()
	if _, err := s.store().Set(context.Background(), scope, at, plaintext, nil); err != nil {
		t.Fatalf("Set(%s) = %v", at, err)
	}
}

func (s stores) publish(t *testing.T, scope values.Scope, environment, name string, binding *bindingsv1.Binding) {
	t.Helper()
	encoded, err := protojson.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	pair := values.Pair{Record: encoded, Value: encoded, Owner: "ocel"}
	if _, err := s.store().SetBinding(context.Background(), scope, environment, "ocel", name, pair); err != nil {
		t.Fatalf("SetBinding(%s) = %v", name, err)
	}
}

func resolved(t *testing.T, held *Values) map[string]string {
	t.Helper()
	if err := held.Join(held.Prefetch(context.Background())); err != nil {
		t.Fatalf("Prefetch() = %v", err)
	}
	var pushed struct {
		Values map[string]string `json:"values"`
	}
	sink := &sink{}
	held.Attach(sink)
	if len(sink.lines) != 1 {
		t.Fatalf("the child was pushed %d generations, want the one the prefetch resolved", len(sink.lines))
	}
	if err := json.Unmarshal([]byte(sink.lines[0]), &pushed); err != nil {
		t.Fatalf("the child was pushed %q, which it cannot read: %v", sink.lines[0], err)
	}
	return pushed.Values
}

type sink struct{ lines []string }

func (s *sink) Write(p []byte) (int, error) {
	s.lines = append(s.lines, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

func manifestOf(keys ...string) vars.Manifest {
	held := vars.Manifest{Project: "acme-prod", Region: "europe-west1", Namespace: "ocel", Slug: "shop", Class: "production"}
	for _, key := range keys {
		held.Keys = append(held.Keys, rt.Key{Key: key})
	}
	return held
}

func TestASecretIsOpenedFromTheProjectsOwnRecordsUnderTheClassKey(t *testing.T) {
	t.Parallel()
	held := fakeStores()
	scope := values.Scope{Project: "shop", Class: providerkit.ClassProduction}
	held.set(t, scope, values.Coordinate{Cell: values.Cell{Key: "DATABASE_URL"}}, "postgres://live")
	held.set(t, scope, values.Coordinate{Cell: values.Cell{Key: "SESSION_SECRET", Folder: "/web"}}, "s3ss10n")

	manifest := manifestOf("DATABASE_URL")
	manifest.Keys = append(manifest.Keys, rt.Key{Key: "SESSION_SECRET", Folder: "/web"})
	got := resolved(t, Over(manifest, held.records, held.sealer))

	if got["DATABASE_URL"] != "postgres://live" || got["SESSION_SECRET"] != "s3ss10n" {
		t.Errorf("resolved %v, want both pinned cells opened, the folder-scoped one under its folder", got)
	}
}

func TestAPreviewReadsItsEnvironmentsValueOverTheClassWideOne(t *testing.T) {
	t.Parallel()
	held := fakeStores()
	scope := values.Scope{Project: "shop", Class: providerkit.ClassPreview}
	held.set(t, scope, values.Coordinate{Cell: values.Cell{Key: "MARK"}}, "class-wide")
	held.set(t, scope, values.Coordinate{Cell: values.Cell{Key: "MARK"}, Environment: "pr-7"}, "pr-7-only")

	manifest := manifestOf("MARK")
	manifest.Class, manifest.Environment = "preview", "pr-7"
	if got := resolved(t, Over(manifest, held.records, held.sealer)); got["MARK"] != "pr-7-only" {
		t.Errorf("resolved MARK=%q, want the preview environment's own value to shadow the class-wide one", got["MARK"])
	}
}

func TestAnUnsetSecretIsReportedMissingRatherThanResolvedEmpty(t *testing.T) {
	t.Parallel()
	held := fakeStores()

	values := Over(manifestOf("DATABASE_URL"), held.records, held.sealer)
	if err := values.Join(values.Prefetch(context.Background())); err != nil {
		t.Fatalf("Prefetch() = %v, want a cell nothing is stored for to resolve to nothing rather than fail", err)
	}
	if missing := values.Missing(); len(missing) != 1 || missing[0] != "DATABASE_URL" {
		t.Errorf("Missing() = %v, want the one key nothing is stored for: the runtime refuses to start the app without it", missing)
	}
}

func TestABindingRecordReachesTheAppUnderTheKeyTheSdkReadsItBy(t *testing.T) {
	t.Parallel()
	held := fakeStores()
	scope := values.Scope{Project: "shop", Class: providerkit.ClassProduction}
	held.publish(t, scope, "", "db--main", &bindingsv1.Binding{
		Name:       "db--main",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "h", Database: "d", Username: "u"}},
	})

	manifest := manifestOf()
	manifest.Bindings = []rt.Binding{{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES}}
	got := resolved(t, Over(manifest, held.records, held.sealer))

	record := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(got["OCEL_RESOURCE_POSTGRES_main"]), record); err != nil {
		t.Fatalf("the app was handed %q under the binding's key, which is no record: %v", got["OCEL_RESOURCE_POSTGRES_main"], err)
	}
	if record.GetPostgres().GetHost() != "h" {
		t.Errorf("the app was handed a record for host %q, want the record published under db--main", record.GetPostgres().GetHost())
	}
}

func TestAManifestNamingNothingLiveBuildsNoStoreClient(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(manifestOf())
	if err != nil {
		t.Fatal(err)
	}
	held, err := FromManifest(raw)
	if err != nil {
		t.Fatalf("FromManifest() = %v", err)
	}
	if held != nil {
		t.Error("a manifest naming no key and no binding built a store client anyway")
	}
	if err := held.Join(held.Prefetch(context.Background())); err != nil {
		t.Errorf("Prefetch() on nothing live = %v, want nil", err)
	}
}

func TestAManifestThatWillNotParseIsAnInitFailure(t *testing.T) {
	t.Parallel()
	if _, err := FromManifest([]byte("{not json")); err == nil {
		t.Fatal("FromManifest() on garbage = nil, want the runtime to refuse to start rather than run the app without its values")
	}
}

func TestTheManifestDrivesTheFirestoreAndKmsClientsTheRuntimeOpens(t *testing.T) {
	t.Parallel()
	endpoint := servingFirestoreAndKMS(t)
	clients := &ports.Clients{Namespace: "ocel", Project: "acme-prod", Region: "europe-west1", Endpoint: endpoint}
	seeded := stores{records: ports.Records{Clients: clients}, sealer: ports.Cipher{Clients: clients}}
	scope := values.Scope{Project: "shop", Class: providerkit.ClassProduction}
	seeded.set(t, scope, values.Coordinate{Cell: values.Cell{Key: "DATABASE_URL"}}, "postgres://through-kms")

	manifest := manifestOf("DATABASE_URL")
	manifest.Endpoint = endpoint
	raw, err := vars.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	held, err := FromManifest(raw)
	if err != nil {
		t.Fatalf("FromManifest() = %v", err)
	}
	if got := resolved(t, held); got["DATABASE_URL"] != "postgres://through-kms" {
		t.Errorf("resolved %v, want the value read out of the Firestore database and opened by the class key the manifest names", got)
	}
}
