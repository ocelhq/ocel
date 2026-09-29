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
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
	variables "github.com/ocelhq/ocel/platform/vps/provider/live"
)

type memKeyValues struct {
	mu      sync.Mutex
	entries map[string]keyvalue.Entry
}

func (m *memKeyValues) Read(_ context.Context, name keyvalue.Key) (keyvalue.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.entries[name.String()]
	if !ok {
		return keyvalue.Entry{}, keyvalue.ErrNotFound
	}
	return stored, nil
}

func (m *memKeyValues) Write(_ context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.put(entry)
}

func (m *memKeyValues) put(entry keyvalue.Entry) (keyvalue.Revision, error) {
	if m.entries == nil {
		m.entries = map[string]keyvalue.Entry{}
	}
	if stored, ok := m.entries[entry.Key.String()]; ok && stored.Revision != entry.Revision {
		return "", keyvalue.ErrStale
	}
	minted := make([]byte, 16)
	if _, err := rand.Read(minted); err != nil {
		return "", err
	}
	entry.Revision = keyvalue.Revision(hex.EncodeToString(minted))
	m.entries[entry.Key.String()] = entry
	return entry.Revision, nil
}

func (m *memKeyValues) WritePair(_ context.Context, first, second keyvalue.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.put(first); err != nil {
		return err
	}
	_, err := m.put(second)
	return err
}

func (m *memKeyValues) Remove(_ context.Context, name keyvalue.Key, _ keyvalue.Revision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, name.String())
	return nil
}

func (m *memKeyValues) List(_ context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []keyvalue.Entry
	for _, entry := range m.entries {
		if entry.Key.Partition.String() != in.String() || len(entry.Key.Path) < len(under) || !slices.Equal(entry.Key.Path[:len(under)], under) {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

type goCipher struct{ key []byte }

func (s goCipher) Seal(_ context.Context, _ environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
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
	return append(nonce, gcm.Seal(nil, nonce, plaintext, bound.Bytes())...), nil
}

func (s goCipher) Open(_ context.Context, _ environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	return variables.Open(s.key, bound, sealed)
}

type box struct {
	tierRoot     string
	stateRoot    string
	routingTable string
	keyValues    *memKeyValues
	cipher       goCipher
}

func aBox(t *testing.T, root string) *box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	b := &box{tierRoot: filepath.Join(root, "etc"), stateRoot: filepath.Join(root, "state"), keyValues: &memKeyValues{}, cipher: goCipher{key: key}}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if err := os.MkdirAll(filepath.Dir(variables.KeyPath(b.tierRoot, tier)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(variables.KeyPath(b.tierRoot, tier), key, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func (b *box) store() variablestore.Store {
	return variablestore.Store{KeyValues: b.keyValues, Cipher: b.cipher}
}

func (b *box) set(t *testing.T, scope variablestore.Scope, at variablestore.Coordinate, plaintext string) {
	t.Helper()
	if _, err := b.store().Set(context.Background(), scope, at, plaintext, nil); err != nil {
		t.Fatal(err)
	}
}

func (b *box) bind(t *testing.T, scope variablestore.Scope, environment, name string, binding *bindingsv1.Binding) {
	t.Helper()
	pair, err := variablestoreserver.BindingPair("terraform", binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.store().SetBinding(context.Background(), scope, environment, "terraform", name, pair); err != nil {
		t.Fatal(err)
	}
}

func (b *box) dump(t *testing.T) {
	t.Helper()
	b.keyValues.mu.Lock()
	defer b.keyValues.mu.Unlock()
	for _, entry := range b.keyValues.entries {
		encoded, err := variables.PathOf(entry.Key)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(variables.KeyValuesDir(b.stateRoot, entry.Key.Partition.Tier), encoded+variables.EntrySuffix)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"revision":"`+string(entry.Revision)+`","value":`+string(entry.Value)+"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (b *box) resolver() Store {
	return Store{TierRoot: b.tierRoot, StateRoot: b.stateRoot, RoutingTable: b.routingTable}
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

func aStoreManifest(sealed string) variables.Manifest {
	return variables.Manifest{
		Slug: "shop", Tier: "production",
		Keys: []live.Key{{Key: "DATABASE_URL"}},
		Store: &variables.Store{
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
	if resolved[variables.StorePublicKey] != "https://storage.shop.example.com" {
		t.Errorf("Resolve() handed %q under %s, want the name this box claims for the store right now",
			resolved[variables.StorePublicKey], variables.StorePublicKey)
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
	if address := resolved[variables.StorePublicKey]; address != "" {
		t.Errorf("Resolve() handed %q as the store's public address on a box claiming nothing for it", address)
	}
}

var shop = variablestore.Scope{Project: "shop", Tier: environment.TierProduction}

func TestTheStoreResolvesEachKeyOffTheBoxUnderTheCallersOwnScopeAndEnvironment(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	b.set(t, shop, variablestore.Coordinate{Cell: variablestore.Cell{Key: "DATABASE_URL"}}, "postgres://tier-wide")
	b.set(t, shop, variablestore.Coordinate{Cell: variablestore.Cell{Key: "DATABASE_URL"}, Environment: "pr-7"}, "postgres://pr-7")
	b.set(t, shop, variablestore.Coordinate{Cell: variablestore.Cell{Key: "SESSION", Folder: "/web"}}, "s3cret")
	b.set(t, variablestore.Scope{Project: "other", Tier: environment.TierProduction}, variablestore.Coordinate{Cell: variablestore.Cell{Key: "DATABASE_URL"}}, "postgres://other")
	b.dump(t)

	resolved, err := b.resolver().Resolve(context.Background(), variables.Manifest{
		Slug: "shop", Tier: "production",
		Keys: []live.Key{{Key: "DATABASE_URL"}, {Key: "SESSION", Folder: "/web"}, {Key: "MISSING"}},
	})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if resolved["DATABASE_URL"] != "postgres://tier-wide" || resolved["SESSION"] != "s3cret" {
		t.Errorf("Resolve() = %v, want the tier-wide value and the folder's", resolved)
	}
	if _, found := resolved["MISSING"]; found {
		t.Errorf("Resolve() handed back MISSING, which nothing stored: the runtime is what says it is unset")
	}
	preview, err := b.resolver().Resolve(context.Background(), variables.Manifest{
		Slug: "shop", Tier: "production", Environment: "pr-7", Keys: []live.Key{{Key: "DATABASE_URL"}},
	})
	if err != nil || preview["DATABASE_URL"] != "postgres://pr-7" {
		t.Errorf("Resolve() for pr-7 = %v, %v, want the environment's own value over the tier-wide one", preview, err)
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
	resolved, err := b.resolver().Resolve(context.Background(), variables.Manifest{Slug: "shop", Tier: "production", Bindings: bindings})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	entry := resolved["OCEL_RESOURCE_POSTGRES_main"]
	if !strings.Contains(entry, "hunter2") || !strings.Contains(entry, "db.internal") {
		t.Errorf("Resolve() handed %q under the binding's key, want the full entry the app connects with", entry)
	}
	if err := live.Conform(bindings, resolved); err != nil {
		t.Errorf("what the store resolved does not conform to what the runtime was built to read: %v", err)
	}
}

func TestTheStoreOpensNothingUnderATierWhoseKeyIsGone(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	b.set(t, shop, variablestore.Coordinate{Cell: variablestore.Cell{Key: "DATABASE_URL"}}, "postgres://tier-wide")
	b.dump(t)
	if err := os.Remove(variables.KeyPath(b.tierRoot, environment.TierProduction)); err != nil {
		t.Fatal(err)
	}
	_, err := b.resolver().Resolve(context.Background(), variables.Manifest{Slug: "shop", Tier: "production", Keys: []live.Key{{Key: "DATABASE_URL"}}})
	if err == nil || !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "seal key") {
		t.Errorf("Resolve() with no key = %v, want a refusal naming the key", err)
	}
}

func TestTheStoreOpensTheObjectStoreCredentialSealedIntoTheCallersManifest(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	bound, err := variables.NewStoreSecretAssociatedData("shop", environment.TierProduction, "shop-prod")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.cipher.Seal(context.Background(), environment.TierProduction, bound, []byte("s3cr3t"))
	if err != nil {
		t.Fatal(err)
	}
	b.bind(t, shop, "", "uploads", &bindingsv1.Binding{
		Name: "uploads", Source: "terraform",
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "shop-prod-uploads"}},
	})
	b.dump(t)

	resolved, err := b.resolver().Resolve(context.Background(), variables.Manifest{
		Slug: "shop", Tier: "production",
		Bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		Store: &variables.Store{
			Env: "shop-prod", Endpoint: "http://shop-prod-store-s3:9000", Region: "us-east-1",
			AccessKeyID: "ocel", Sealed: base64.StdEncoding.EncodeToString(sealed),
		},
	})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if resolved[variables.StoreSecretKey] != "s3cr3t" {
		t.Errorf("Resolve() handed %q under %s, want the store credential the deploy sealed for this box's runtime",
			resolved[variables.StoreSecretKey], variables.StoreSecretKey)
	}
}

func TestTheStoreRefusesAStoreCredentialSealedForAnotherProject(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	elsewhere, err := variables.NewStoreSecretAssociatedData("other", environment.TierProduction, "other-prod")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.cipher.Seal(context.Background(), environment.TierProduction, elsewhere, []byte("s3cr3t"))
	if err != nil {
		t.Fatal(err)
	}
	b.dump(t)
	_, err = b.resolver().Resolve(context.Background(), variables.Manifest{
		Slug: "shop", Tier: "production",
		Keys:  []live.Key{{Key: "DATABASE_URL"}},
		Store: &variables.Store{Env: "shop-prod", Sealed: base64.StdEncoding.EncodeToString(sealed)},
	})
	if err == nil || !strings.Contains(err.Error(), variables.StoreSecretName) {
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
	resolved, err := b.resolver().Resolve(context.Background(), variables.Manifest{
		Slug: "shop", Tier: "production", Bindings: bindings,
		Store: aStoreManifest("").Store,
	})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	entry := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(resolved["OCEL_RESOURCE_BUCKET_uploads"]), entry); err != nil {
		t.Fatalf("the binding the runtime reads is no entry: %v", err)
	}
	return entry.GetBucket()
}

func TestAPublicBucketIsDeliveredTheAddressTheBoxClaimsForItNow(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	bindings := aBucketBinding(t, b, true)

	bucket := resolvedBucket(t, b, bindings)
	if got, want := bucket.GetPublicBaseUrl(), "https://storage.shop.example.com/shop-prod-uploads"; got != want {
		t.Errorf("publicBaseUrl = %q, want %q: a public bucket's address is whatever the box claims for its store right now", got, want)
	}
}

func TestABucketThatWasNeverDeclaredPublicIsDeliveredNoPublicAddress(t *testing.T) {
	t.Parallel()
	b := aBox(t, t.TempDir())
	bindings := aBucketBinding(t, b, false)

	if address := resolvedBucket(t, b, bindings).GetPublicBaseUrl(); address != "" {
		t.Errorf("publicBaseUrl = %q on a bucket nothing serves anonymously, so every url it hands out would be refused", address)
	}
}
