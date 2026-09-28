package live

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/seal"
)

func complete() Manifest {
	return Manifest{Slug: "shop", Tier: "production", Keys: []live.Key{{Key: "DATABASE_URL"}}}
}

func TestAManifestNamingNothingLiveRendersToNothing(t *testing.T) {
	t.Parallel()
	manifest := complete()
	manifest.Keys = nil
	rendered, err := Render(manifest)
	if err != nil || rendered != nil {
		t.Errorf("Render() = %q, %v, want nothing: a container with no live value boots with no manifest and dials no socket", rendered, err)
	}
}

func TestARenderedManifestParsesBackToWhatWasPinned(t *testing.T) {
	t.Parallel()
	manifest := complete()
	manifest.Tier, manifest.Environment = "preview", "pr-7"
	manifest.Keys = append(manifest.Keys, live.Key{Key: "SESSION", Folder: "/web"})
	rendered, err := Render(manifest)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if parsed.Slug != "shop" || parsed.Tier != "preview" || parsed.Environment != "pr-7" ||
		len(parsed.Keys) != 2 || parsed.Keys[1].Folder != "/web" {
		t.Errorf("Parse(Render()) = %+v, want %+v", parsed, manifest)
	}
}

func TestAManifestMissingWhatScopesTheStoreIsRefused(t *testing.T) {
	t.Parallel()
	for name, sabotage := range map[string]func(*Manifest){
		"slug":                     func(m *Manifest) { m.Slug = "" },
		"tier":                     func(m *Manifest) { m.Tier = "" },
		"a tier nothing is called": func(m *Manifest) { m.Tier = "staging" },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := complete()
			sabotage(&manifest)
			if _, err := Render(manifest); err == nil {
				t.Errorf("Render() without %s = nil, want a refusal: the agent would look for the value under no scope at all", name)
			}
		})
	}
}

func sealFor(t *testing.T, key []byte, bound seal.AssociatedData, plaintext string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, sealNonce)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return append(nonce, gcm.Seal(nil, nonce, []byte(plaintext), bound.Bytes())...)
}

func aKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, sealKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

var bound = seal.AssociatedData{
	{Name: "project", Value: "shop"},
	{Name: "class", Value: "production"},
	{Name: "environment", Value: "*"},
	{Name: "folder", Value: "/"},
	{Name: "binding", Value: ""},
	{Name: "key", Value: "DATABASE_URL"},
}

func TestAValueOpensUnderItsOwnAssociatedDataAndNoOther(t *testing.T) {
	t.Parallel()
	key := aKey(t)
	sealed := sealFor(t, key, bound, "postgres://example")

	opened, err := Open(key, bound, sealed)
	if err != nil || string(opened) != "postgres://example" {
		t.Fatalf("Open() = %q, %v", opened, err)
	}
	elsewhere := slices.Clone(bound)
	elsewhere[0].Value = "other"
	if _, err := Open(key, elsewhere, sealed); err == nil {
		t.Error("a value sealed for shop opened for other, so the associated data authenticates nothing")
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := Open(key, bound, sealed); err == nil {
		t.Error("a sealed value whose bytes moved opened anyway")
	}
	if _, err := Open(key[:16], bound, sealed); err == nil {
		t.Error("a key narrower than AES-256 opened something")
	}
}

const (
	keySealedWith      = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	valueSealedAlready = "oKGio6SlpqeoqaqrlXMjQSy9Z+ARAOShYg78hOZr+SZv+OoHsggDIp1z"
)

func TestAValueOnTheBoxOpensUnderTheKeyAndAssociatedDataItWasSealedWith(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key, err := base64.StdEncoding.DecodeString(keySealedWith)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.StdEncoding.DecodeString(valueSealedAlready)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "production"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "production", "seal.key"), key, 0o400); err != nil {
		t.Fatal(err)
	}
	at := seal.AssociatedData{
		{Name: "project", Value: "shop"},
		{Name: "class", Value: "production"},
		{Name: "environment", Value: "staging"},
		{Name: "folder", Value: "/web"},
		{Name: "binding", Value: ""},
		{Name: "key", Value: "STRIPE_API_KEY"},
	}
	opened, err := Cipher{Root: root}.Open(context.Background(), environment.TierProduction, at, sealed)
	if err != nil || string(opened) != "sk_live_secret" {
		t.Fatalf("Open() = %q, %v, want the value sealed at shop/production/staging/%%2Fweb//STRIPE_API_KEY/ to open: every value on a box is bound to those bytes", opened, err)
	}
}

func TestTheBoxCipherReadsTheTierKeyWhereBootstrapMintsIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := aKey(t)
	if err := os.MkdirAll(filepath.Dir(KeyPath(root, environment.TierProduction)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(KeyPath(root, environment.TierProduction), key, 0o400); err != nil {
		t.Fatal(err)
	}
	vault := Cipher{Root: root}
	opened, err := vault.Open(context.Background(), environment.TierProduction, bound, sealFor(t, key, bound, "hunter2"))
	if err != nil || string(opened) != "hunter2" {
		t.Fatalf("Open() = %q, %v", opened, err)
	}
	if _, err := vault.Open(context.Background(), environment.TierPreview, bound, sealFor(t, key, bound, "hunter2")); err == nil {
		t.Error("a preview value opened under the production key, and each tier is sealed to its own")
	}
	if _, err := vault.Seal(context.Background(), environment.TierProduction, bound, []byte("x")); err == nil {
		t.Error("the box-side cipher sealed something, and sealing is the helper's under sudo alone")
	}
}

func writeEntry(t *testing.T, root string, key keyvalue.Key, value string) {
	t.Helper()
	path, err := PathOf(key)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(RecordsDir(root, key.Partition.Tier), path+EntrySuffix)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"revision":"0123456789abcdef0123456789abcdef","value":`+value+"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

var shop = keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootValues, Path: []string{"shop"}}

func TestTheEntriesAreReadOffTheTierTheHelperWritesAndNeverWritten(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	one := shop.Key("cells", "/", "DATABASE_URL", "*")
	two := shop.Key("cells", "/apps/web", "SESSION", "pr-7")
	writeEntry(t, root, one, `"one"`)
	writeEntry(t, root, two, `"two"`)
	store := KeyValues{Root: root}

	read, err := store.Read(context.Background(), one)
	if err != nil || string(read.Value) != `"one"` || read.Revision == "" {
		t.Fatalf("Read() = %+v, %v", read, err)
	}
	if _, err := store.Read(context.Background(), shop.Key("cells", "/", "MISSING", "*")); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("Read() of nothing = %v, want %v", err, keyvalue.ErrNotFound)
	}
	listed, err := store.List(context.Background(), shop, "cells")
	if err != nil || len(listed) != 2 {
		t.Fatalf("List() = %v, %v, want both cells", listed, err)
	}
	for _, entry := range listed {
		if entry.Key.String() != one.String() && entry.Key.String() != two.String() {
			t.Errorf("List() named %s, which nothing wrote", entry.Key)
		}
	}
	other := keyvalue.Partition{Tier: shop.Tier, Root: shop.Root, Path: []string{"other"}}
	empty, err := store.List(context.Background(), other, "cells")
	if err != nil || len(empty) != 0 {
		t.Errorf("List() in another project = %v, %v, want nothing", empty, err)
	}
	if _, err := store.Write(context.Background(), read); err == nil {
		t.Error("the box-side entries wrote something, and a deploy writes through the helper alone")
	}
	if err := store.Remove(context.Background(), one, read.Revision); err == nil {
		t.Error("the box-side entries removed something")
	}
}

func TestAKeyRoundTripsThroughThePathAFileAnswersTo(t *testing.T) {
	t.Parallel()
	for _, key := range []keyvalue.Key{
		{Partition: keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootConformance, Path: []string{"TestOne/sub"}}, Path: []string{"leaf"}},
		shop.Key("cells", "/apps/web", "DATABASE_URL"),
		{Partition: keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootLedger, Path: []string{"a+b"}}, Path: []string{".hidden", ".."}},
	} {
		path, err := PathOf(key)
		if err != nil {
			t.Fatalf("PathOf(%s) = %v", key, err)
		}
		if strings.Contains(path, "/.") || strings.HasPrefix(path, ".") {
			t.Errorf("PathOf(%s) = %q, and a segment that starts a dot names something no entry is", key, path)
		}
		dir, rest, _ := strings.Cut(path, "/")
		if want, err := PartitionDir(key.Partition); err != nil || dir != want {
			t.Errorf("PathOf(%s) = %q, want it under the partition's own directory %q", key, path, want)
		}
		decoded, err := KeyOf(key.Partition, rest)
		if err != nil || decoded.String() != key.String() {
			t.Errorf("KeyOf(PathOf(%s)) = %s, %v", key, decoded, err)
		}
	}
}

func TestTwoPartitionsNeverShareADirectory(t *testing.T) {
	t.Parallel()
	one, err := PartitionDir(keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootStacks, Path: []string{"a+b"}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := PartitionDir(keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootStacks, Path: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Errorf("both partitions keep their entries in %q", one)
	}
}
