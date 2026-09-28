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
	"github.com/ocelhq/ocel/pkg/records"
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

func writeRecord(t *testing.T, root string, name records.Name, body string) {
	t.Helper()
	tier, encoded, err := Located(name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(RecordsDir(root, tier), encoded+recordSuffix)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef\n"+base64.StdEncoding.EncodeToString([]byte(body))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheRecordsAreReadOffTheTierTheHelperWritesAndNeverWritten(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	one := records.Name{"values", "shop", "production", "cells", "/", "DATABASE_URL", "*"}
	two := records.Name{"values", "shop", "production", "cells", "/apps/web", "SESSION", "pr-7"}
	writeRecord(t, root, one, "one")
	writeRecord(t, root, two, "two")
	store := Records{Root: root}

	read, err := store.Read(context.Background(), one)
	if err != nil || string(read.Bytes) != "one" || read.Revision == "" {
		t.Fatalf("Read() = %+v, %v", read, err)
	}
	if _, err := store.Read(context.Background(), records.Name{"values", "shop", "production", "cells", "/", "MISSING", "*"}); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() of nothing = %v, want %v", err, records.ErrNotFound)
	}
	listed, err := store.List(context.Background(), records.Name{"values", "shop", "production", "cells"})
	if err != nil || len(listed) != 2 {
		t.Fatalf("List() = %v, %v, want both cells", listed, err)
	}
	for _, record := range listed {
		if record.Name.String() != one.String() && record.Name.String() != two.String() {
			t.Errorf("List() named %s, which nothing wrote", record.Name)
		}
	}
	empty, err := store.List(context.Background(), records.Name{"values", "other", "production", "cells"})
	if err != nil || len(empty) != 0 {
		t.Errorf("List() under another project = %v, %v, want nothing", empty, err)
	}
	if _, err := store.Write(context.Background(), read); err == nil {
		t.Error("the box-side records wrote something, and a deploy writes through the helper alone")
	}
	if err := store.Remove(context.Background(), one, read.Revision); err == nil {
		t.Error("the box-side records removed something")
	}
}

func TestARecordNameRoundTripsThroughTheNameAFileAnswersTo(t *testing.T) {
	t.Parallel()
	for _, name := range []records.Name{
		{"conformance", "production", "TestOne/sub", "leaf"},
		{"values", "shop", "production", "/apps/web", "DATABASE_URL"},
		{"ledger", "production/shop", ".hidden", ".."},
	} {
		encoded, err := EncodeName(name)
		if err != nil {
			t.Fatalf("EncodeName(%s) = %v", name, err)
		}
		if strings.Contains(encoded, "/.") {
			t.Errorf("EncodeName(%s) = %q, and a segment that starts a dot names something no record is", name, encoded)
		}
		decoded, err := DecodeName(encoded)
		if err != nil || decoded.String() != name.String() {
			t.Errorf("DecodeName(EncodeName(%s)) = %s, %v", name, decoded, err)
		}
	}
}
