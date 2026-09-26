package agent

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/envvarsserver"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

type memRecords struct {
	mu   sync.Mutex
	held map[string]records.Record
}

func (m *memRecords) Read(_ context.Context, name records.Name) (records.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.held[name.String()]
	if !ok {
		return records.Record{}, records.ErrNotFound
	}
	return held, nil
}

func (m *memRecords) Write(_ context.Context, record records.Record) (records.Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.put(record)
}

func (m *memRecords) put(record records.Record) (records.Revision, error) {
	if m.held == nil {
		m.held = map[string]records.Record{}
	}
	if held, ok := m.held[record.Name.String()]; ok && held.Revision != record.Revision {
		return "", records.ErrStale
	}
	minted := make([]byte, 16)
	if _, err := rand.Read(minted); err != nil {
		return "", err
	}
	record.Revision = records.Revision(hex.EncodeToString(minted))
	m.held[record.Name.String()] = record
	return record.Revision, nil
}

func (m *memRecords) WritePair(_ context.Context, first, second records.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.put(first); err != nil {
		return err
	}
	_, err := m.put(second)
	return err
}

func (m *memRecords) Remove(_ context.Context, name records.Name, _ records.Revision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.held, name.String())
	return nil
}

func (m *memRecords) List(_ context.Context, under records.Name) ([]records.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []records.Record
	for _, record := range m.held {
		if _, beneath := record.Name.Under(under); beneath {
			out = append(out, record)
		}
	}
	return out, nil
}

type goSealer struct{ key []byte }

func (s goSealer) Seal(_ context.Context, at records.SealScope, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plaintext, at.AAD())...), nil
}

func (s goSealer) Open(_ context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
	return vars.Open(s.key, at, sealed)
}

type box struct {
	classRoot    string
	stateRoot    string
	routingTable string
	records      *memRecords
	sealer       goSealer
}

func aBox(t *testing.T, root string) *box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	b := &box{classRoot: filepath.Join(root, "etc"), stateRoot: filepath.Join(root, "state"), records: &memRecords{}, sealer: goSealer{key: key}}
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		if err := os.MkdirAll(filepath.Dir(vars.KeyPath(b.classRoot, class)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vars.KeyPath(b.classRoot, class), key, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func (b *box) store() envvars.Store { return envvars.Store{Records: b.records, Cipher: b.sealer} }

func (b *box) set(t *testing.T, scope envvars.Scope, at envvars.Coordinate, plaintext string) {
	t.Helper()
	if _, err := b.store().Set(context.Background(), scope, at, plaintext, nil); err != nil {
		t.Fatal(err)
	}
}

func (b *box) bind(t *testing.T, scope envvars.Scope, environment, name string, binding *bindingsv1.Binding) {
	t.Helper()
	pair, err := envvarsserver.BindingPair("terraform", binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.store().SetBinding(context.Background(), scope, environment, "terraform", name, pair); err != nil {
		t.Fatal(err)
	}
}

func (b *box) dump(t *testing.T) {
	t.Helper()
	b.records.mu.Lock()
	defer b.records.mu.Unlock()
	for _, record := range b.records.held {
		class, encoded, err := vars.Located(record.Name)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(vars.RecordsDir(b.stateRoot, class), encoded+".rec")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(string(record.Revision)+"\n"+base64.StdEncoding.EncodeToString(record.Bytes)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (b *box) resolver() Store {
	return Store{ClassRoot: b.classRoot, StateRoot: b.stateRoot, RoutingTable: b.routingTable}
}

func (b *box) claims(t *testing.T, document string) {
	t.Helper()
	b.routingTable = filepath.Join(b.stateRoot, "routing.json")
	if err := os.MkdirAll(b.stateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.routingTable, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
}

func aStoreManifest(sealed string) vars.Manifest {
	return vars.Manifest{
		Slug: "shop", Class: "production",
		Keys: []live.Key{{Key: "DATABASE_URL"}},
		Store: &vars.Store{
			Env: "shop-prod", Endpoint: "http://shop-prod-store-s3:9000", Region: "us-east-1",
			AccessKeyID: "ocel", Pointer: "@production", Sealed: sealed,
		},
	}
}

const claimingStorage = `{"grace":"30s","claims":[
	{"hostname":"shop.example.com","owner":"ocel--shop--production","pointer":"@production","app":"web"},
	{"hostname":"storage.shop.example.com","owner":"ocel--shop--production","pointer":"@production","app":"storage"}]}`

func TestTheStoresPublicAddressIsWhateverTheBoxClaimsForItNow(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	b.dump(t)
	b.claims(t, claimingStorage)

	resolved, err := b.resolver().Resolve(context.Background(), aStoreManifest(""))
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if resolved[vars.StorePublicKey] != "https://storage.shop.example.com" {
		t.Errorf("Resolve() handed %q under %s, want the name this box claims for the store right now",
			resolved[vars.StorePublicKey], vars.StorePublicKey)
	}
}

func TestAStoreNoDomainPointsAtHasNoPublicAddress(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	b.dump(t)
	b.claims(t, `{"grace":"30s"}`)

	resolved, err := b.resolver().Resolve(context.Background(), aStoreManifest(""))
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if held := resolved[vars.StorePublicKey]; held != "" {
		t.Errorf("Resolve() handed %q as the store's public address on a box claiming nothing for it", held)
	}
}

var shop = envvars.Scope{Project: "shop", Class: edge.ClassProduction}

func TestTheStoreResolvesEachKeyOffTheBoxUnderTheCallersOwnScopeAndEnvironment(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	b.set(t, shop, envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}}, "postgres://class-wide")
	b.set(t, shop, envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}, Environment: "pr-7"}, "postgres://pr-7")
	b.set(t, shop, envvars.Coordinate{Cell: envvars.Cell{Key: "SESSION", Folder: "/web"}}, "s3cret")
	b.set(t, envvars.Scope{Project: "other", Class: edge.ClassProduction}, envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}}, "postgres://other")
	b.dump(t)

	resolved, err := b.resolver().Resolve(context.Background(), vars.Manifest{
		Slug: "shop", Class: "production",
		Keys: []live.Key{{Key: "DATABASE_URL"}, {Key: "SESSION", Folder: "/web"}, {Key: "MISSING"}},
	})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if resolved["DATABASE_URL"] != "postgres://class-wide" || resolved["SESSION"] != "s3cret" {
		t.Errorf("Resolve() = %v, want the class-wide value and the folder's", resolved)
	}
	if _, held := resolved["MISSING"]; held {
		t.Errorf("Resolve() handed back MISSING, which nothing stored: the runtime is what says it is unset")
	}
	preview, err := b.resolver().Resolve(context.Background(), vars.Manifest{
		Slug: "shop", Class: "production", Environment: "pr-7", Keys: []live.Key{{Key: "DATABASE_URL"}},
	})
	if err != nil || preview["DATABASE_URL"] != "postgres://pr-7" {
		t.Errorf("Resolve() for pr-7 = %v, %v, want the environment's own value over the class-wide one", preview, err)
	}
}

func TestTheStoreResolvesABindingRecordUnderTheKeyTheRuntimeReadsItBy(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	b.bind(t, shop, "", "main", &bindingsv1.Binding{
		Name:   "main",
		Source: "terraform",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: "db.internal", Port: 5432, Database: "orders", Username: "app", Password: "hunter2",
		}},
	})
	b.dump(t)

	bindings := []live.Binding{{Name: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES}}
	resolved, err := b.resolver().Resolve(context.Background(), vars.Manifest{Slug: "shop", Class: "production", Bindings: bindings})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	record := resolved["OCEL_RESOURCE_POSTGRES_main"]
	if !strings.Contains(record, "hunter2") || !strings.Contains(record, "db.internal") {
		t.Errorf("Resolve() handed %q under the binding's key, want the full record the app connects with", record)
	}
	if err := live.Conform(bindings, resolved); err != nil {
		t.Errorf("what the store resolved does not conform to what the runtime was built to read: %v", err)
	}
}

func TestTheStoreOpensNothingUnderAClassWhoseKeyIsGone(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	b.set(t, shop, envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}}, "postgres://class-wide")
	b.dump(t)
	if err := os.Remove(vars.KeyPath(b.classRoot, edge.ClassProduction)); err != nil {
		t.Fatal(err)
	}
	_, err := b.resolver().Resolve(context.Background(), vars.Manifest{Slug: "shop", Class: "production", Keys: []live.Key{{Key: "DATABASE_URL"}}})
	if err == nil || !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "seal key") {
		t.Errorf("Resolve() with no key = %v, want a refusal naming the key", err)
	}
}

func TestTheStoreOpensTheObjectStoreCredentialSealedIntoTheCallersManifest(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	at := records.SealScope{
		Project: "shop", Class: edge.ClassProduction, Env: "shop-prod",
		Folder: vars.StoreSecretFolder, Binding: vars.StoreSecretBinding, Name: vars.StoreSecretName,
	}
	sealed, err := b.sealer.Seal(context.Background(), at, []byte("s3cr3t"))
	if err != nil {
		t.Fatal(err)
	}
	b.bind(t, shop, "", "uploads", &bindingsv1.Binding{
		Name: "uploads", Source: "terraform",
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "shop-prod-uploads"}},
	})
	b.dump(t)

	resolved, err := b.resolver().Resolve(context.Background(), vars.Manifest{
		Slug: "shop", Class: "production",
		Bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		Store: &vars.Store{
			Env: "shop-prod", Endpoint: "http://shop-prod-store-s3:9000", Region: "us-east-1",
			AccessKeyID: "ocel", Sealed: base64.StdEncoding.EncodeToString(sealed),
		},
	})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if resolved[vars.StoreSecretKey] != "s3cr3t" {
		t.Errorf("Resolve() handed %q under %s, want the store credential the deploy sealed for this box's runtime",
			resolved[vars.StoreSecretKey], vars.StoreSecretKey)
	}
}

func TestTheStoreRefusesAStoreCredentialSealedForAnotherProject(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	elsewhere := records.SealScope{
		Project: "other", Class: edge.ClassProduction, Env: "other-prod",
		Folder: vars.StoreSecretFolder, Binding: vars.StoreSecretBinding, Name: vars.StoreSecretName,
	}
	sealed, err := b.sealer.Seal(context.Background(), elsewhere, []byte("s3cr3t"))
	if err != nil {
		t.Fatal(err)
	}
	b.dump(t)
	_, err = b.resolver().Resolve(context.Background(), vars.Manifest{
		Slug: "shop", Class: "production",
		Keys:  []live.Key{{Key: "DATABASE_URL"}},
		Store: &vars.Store{Env: "shop-prod", Sealed: base64.StdEncoding.EncodeToString(sealed)},
	})
	if err == nil || !strings.Contains(err.Error(), vars.StoreSecretName) {
		t.Errorf("Resolve() = %v, want a refusal: a credential sealed for another project must not open here", err)
	}
}

func aBucketBinding(t *testing.T, b *box, public bool) []live.Binding {
	t.Helper()
	b.bind(t, shop, "", "uploads", &bindingsv1.Binding{
		Name:   "uploads",
		Source: "terraform",
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{
			Bucket: "shop-prod-uploads", Public: public,
		}},
	})
	b.dump(t)
	b.claims(t, claimingStorage)
	return []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}}
}

func resolvedBucket(t *testing.T, b *box, bindings []live.Binding) *bindingsv1.BucketProperties {
	t.Helper()
	resolved, err := b.resolver().Resolve(context.Background(), vars.Manifest{
		Slug: "shop", Class: "production", Bindings: bindings,
		Store: aStoreManifest("").Store,
	})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	record := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(resolved["OCEL_RESOURCE_BUCKET_uploads"]), record); err != nil {
		t.Fatalf("the binding the runtime reads is no record: %v", err)
	}
	return record.GetBucket()
}

func TestAPublicBucketIsDeliveredTheAddressTheBoxClaimsForItNow(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	bindings := aBucketBinding(t, b, true)

	held := resolvedBucket(t, b, bindings)
	if got, want := held.GetPublicBaseUrl(), "https://storage.shop.example.com/shop-prod-uploads"; got != want {
		t.Errorf("publicBaseUrl = %q, want %q: a public bucket's address is whatever the box claims for its store right now", got, want)
	}
}

func TestABucketThatWasNeverDeclaredPublicIsDeliveredNoPublicAddress(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	bindings := aBucketBinding(t, b, false)

	if held := resolvedBucket(t, b, bindings).GetPublicBaseUrl(); held != "" {
		t.Errorf("publicBaseUrl = %q on a bucket nothing serves anonymously, so every url it hands out would be refused", held)
	}
}
