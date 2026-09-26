package live

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vars "github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type stores struct {
	records records.Store
	sealer  records.Cipher
}

func fakeStores() stores {
	return stores{records: fake.NewRecords(), sealer: fake.NewCipher()}
}

func (s stores) store() envvars.Store {
	return envvars.Store{Records: s.records, Cipher: s.sealer}
}

func (s stores) set(t *testing.T, scope envvars.Scope, at envvars.Coordinate, plaintext string) {
	t.Helper()
	if _, err := s.store().Set(context.Background(), scope, at, plaintext, nil); err != nil {
		t.Fatalf("Set(%s) = %v", at, err)
	}
}

func (s stores) publish(t *testing.T, scope envvars.Scope, environment, name string, binding *bindingsv1.Binding) {
	t.Helper()
	encoded, err := protojson.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	pair := envvars.BindingWrite{Record: encoded, Value: encoded, Owner: "ocel"}
	if _, err := s.store().SetBinding(context.Background(), scope, environment, "ocel", name, pair); err != nil {
		t.Fatalf("SetBinding(%s) = %v", name, err)
	}
}

func resolved(t *testing.T, values *live.Values) map[string]string {
	t.Helper()
	if err := values.Join(values.Prefetch(context.Background())); err != nil {
		t.Fatalf("Prefetch() = %v", err)
	}
	var pushed struct {
		Values map[string]string `json:"values"`
	}
	sink := &sink{}
	values.Attach(sink)
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
	manifest := vars.Manifest{Project: "acme-prod", Region: "europe-west1", Namespace: "ocel", Slug: "shop", Class: "production"}
	for _, key := range keys {
		manifest.Keys = append(manifest.Keys, live.Key{Key: key})
	}
	return manifest
}

func TestASecretIsOpenedFromTheProjectsOwnRecordsUnderTheClassKey(t *testing.T) {
	t.Parallel()
	fakes := fakeStores()
	scope := envvars.Scope{Project: "shop", Class: edge.ClassProduction}
	fakes.set(t, scope, envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}}, "postgres://live")
	fakes.set(t, scope, envvars.Coordinate{Cell: envvars.Cell{Key: "SESSION_SECRET", Folder: "/web"}}, "s3ss10n")

	manifest := manifestOf("DATABASE_URL")
	manifest.Keys = append(manifest.Keys, live.Key{Key: "SESSION_SECRET", Folder: "/web"})
	got := resolved(t, Over(manifest, fakes.records, fakes.sealer))

	if got["DATABASE_URL"] != "postgres://live" || got["SESSION_SECRET"] != "s3ss10n" {
		t.Errorf("resolved %v, want both pinned cells opened, the folder-scoped one under its folder", got)
	}
}

func TestAPreviewReadsItsEnvironmentsValueOverTheClassWideOne(t *testing.T) {
	t.Parallel()
	fakes := fakeStores()
	scope := envvars.Scope{Project: "shop", Class: edge.ClassPreview}
	fakes.set(t, scope, envvars.Coordinate{Cell: envvars.Cell{Key: "MARK"}}, "class-wide")
	fakes.set(t, scope, envvars.Coordinate{Cell: envvars.Cell{Key: "MARK"}, Environment: "pr-7"}, "pr-7-only")

	manifest := manifestOf("MARK")
	manifest.Class, manifest.Environment = "preview", "pr-7"
	if got := resolved(t, Over(manifest, fakes.records, fakes.sealer)); got["MARK"] != "pr-7-only" {
		t.Errorf("resolved MARK=%q, want the preview environment's own value to shadow the class-wide one", got["MARK"])
	}
}

func TestAnUnsetSecretIsReportedMissingRatherThanResolvedEmpty(t *testing.T) {
	t.Parallel()
	fakes := fakeStores()

	values := Over(manifestOf("DATABASE_URL"), fakes.records, fakes.sealer)
	if err := values.Join(values.Prefetch(context.Background())); err != nil {
		t.Fatalf("Prefetch() = %v, want a cell nothing is stored for to resolve to nothing rather than fail", err)
	}
	if missing := values.Missing(); len(missing) != 1 || missing[0] != "DATABASE_URL" {
		t.Errorf("Missing() = %v, want the one key nothing is stored for: the runtime refuses to start the app without it", missing)
	}
}

func TestABindingRecordReachesTheAppUnderTheKeyTheSdkReadsItBy(t *testing.T) {
	t.Parallel()
	fakes := fakeStores()
	scope := envvars.Scope{Project: "shop", Class: edge.ClassProduction}
	fakes.publish(t, scope, "", "db--main", &bindingsv1.Binding{
		Name:       "db--main",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "h", Database: "d", Username: "u"}},
	})

	manifest := manifestOf()
	manifest.Bindings = []live.Binding{{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES}}
	got := resolved(t, Over(manifest, fakes.records, fakes.sealer))

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
	values, err := FromManifest(raw)
	if err != nil {
		t.Fatalf("FromManifest() = %v", err)
	}
	if values != nil {
		t.Error("a manifest naming no key and no binding built a store client anyway")
	}
	if err := values.Join(values.Prefetch(context.Background())); err != nil {
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
	scope := envvars.Scope{Project: "shop", Class: edge.ClassProduction}
	seeded.set(t, scope, envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}}, "postgres://through-kms")

	manifest := manifestOf("DATABASE_URL")
	manifest.Endpoint = endpoint
	raw, err := vars.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	values, err := FromManifest(raw)
	if err != nil {
		t.Fatalf("FromManifest() = %v", err)
	}
	if got := resolved(t, values); got["DATABASE_URL"] != "postgres://through-kms" {
		t.Errorf("resolved %v, want the value read out of the Firestore database and opened by the class key the manifest names", got)
	}
}
