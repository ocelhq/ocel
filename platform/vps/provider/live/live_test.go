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
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
)

func complete() Manifest {
	return Manifest{Slug: "shop", Class: "production", Keys: []rt.Key{{Key: "DATABASE_URL"}}}
}

func TestAManifestNamingNothingLiveRendersToNothing(t *testing.T) {
	t.Parallel()
	held := complete()
	held.Keys = nil
	rendered, err := Render(held)
	if err != nil || rendered != nil {
		t.Errorf("Render() = %q, %v, want nothing: a container with no live value boots with no manifest and dials no socket", rendered, err)
	}
}

func TestARenderedManifestParsesBackToWhatWasPinned(t *testing.T) {
	t.Parallel()
	held := complete()
	held.Class, held.Environment = "preview", "pr-7"
	held.Keys = append(held.Keys, rt.Key{Key: "SESSION", Folder: "/web"})
	rendered, err := Render(held)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if parsed.Slug != "shop" || parsed.Class != "preview" || parsed.Environment != "pr-7" ||
		len(parsed.Keys) != 2 || parsed.Keys[1].Folder != "/web" {
		t.Errorf("Parse(Render()) = %+v, want %+v", parsed, held)
	}
}

func TestAManifestMissingWhatScopesTheStoreIsRefused(t *testing.T) {
	t.Parallel()
	for name, sabotage := range map[string]func(*Manifest){
		"slug":                      func(m *Manifest) { m.Slug = "" },
		"class":                     func(m *Manifest) { m.Class = "" },
		"a class nothing is called": func(m *Manifest) { m.Class = "staging" },
	} {
		t.Run(name, func(t *testing.T) {
			held := complete()
			sabotage(&held)
			if _, err := Render(held); err == nil {
				t.Errorf("Render() without %s = nil, want a refusal: the agent would look for the value under no scope at all", name)
			}
		})
	}
}

func sealFor(t *testing.T, key []byte, at providerkit.Coordinate, plaintext string) []byte {
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
	return append(nonce, gcm.Seal(nil, nonce, []byte(plaintext), at.AAD())...)
}

func aKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, sealKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

var bound = providerkit.Coordinate{Project: "shop", Class: providerkit.ClassProduction, Env: "*", Folder: "/", Name: "DATABASE_URL"}

func TestAValueOpensAtItsOwnCoordinateAndNowhereElse(t *testing.T) {
	t.Parallel()
	key := aKey(t)
	sealed := sealFor(t, key, bound, "postgres://example")

	opened, err := Open(key, bound, sealed)
	if err != nil || string(opened) != "postgres://example" {
		t.Fatalf("Open() = %q, %v", opened, err)
	}
	elsewhere := bound
	elsewhere.Project = "other"
	if _, err := Open(key, elsewhere, sealed); err == nil {
		t.Error("a value sealed for shop opened for other, so the coordinate authenticates nothing")
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := Open(key, bound, sealed); err == nil {
		t.Error("a sealed value whose bytes moved opened anyway")
	}
	if _, err := Open(key[:16], bound, sealed); err == nil {
		t.Error("a key narrower than AES-256 opened something")
	}
}

func TestTheSealerReadsTheClassKeyWhereBootstrapMintsIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := aKey(t)
	if err := os.MkdirAll(filepath.Dir(KeyPath(root, bound.Class)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(KeyPath(root, bound.Class), key, 0o400); err != nil {
		t.Fatal(err)
	}
	sealer := Sealer{Root: root}
	opened, err := sealer.Open(context.Background(), bound, sealFor(t, key, bound, "hunter2"))
	if err != nil || string(opened) != "hunter2" {
		t.Fatalf("Open() = %q, %v", opened, err)
	}
	preview := bound
	preview.Class = providerkit.ClassPreview
	if _, err := sealer.Open(context.Background(), preview, sealFor(t, key, preview, "hunter2")); err == nil {
		t.Error("a preview value opened under the production key, and each class is sealed to its own")
	}
	if _, err := sealer.Seal(context.Background(), bound, []byte("x")); err == nil {
		t.Error("the box-side sealer sealed something, and sealing is the helper's under sudo alone")
	}
}

func writeRecord(t *testing.T, root string, name providerkit.RecordName, body string) {
	t.Helper()
	class, encoded, err := Located(name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(RecordsDir(root, class), encoded+recordSuffix)
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
	one := providerkit.RecordName{"values", "shop", "production", "cells", "/", "DATABASE_URL", "*"}
	two := providerkit.RecordName{"values", "shop", "production", "cells", "/apps/web", "SESSION", "pr-7"}
	writeRecord(t, root, one, "one")
	writeRecord(t, root, two, "two")
	records := Records{Root: root}

	held, err := records.Read(context.Background(), one)
	if err != nil || string(held.Bytes) != "one" || held.Revision == "" {
		t.Fatalf("Read() = %+v, %v", held, err)
	}
	if _, err := records.Read(context.Background(), providerkit.RecordName{"values", "shop", "production", "cells", "/", "MISSING", "*"}); !errors.Is(err, providerkit.ErrNoRecord) {
		t.Errorf("Read() of nothing = %v, want %v", err, providerkit.ErrNoRecord)
	}
	listed, err := records.List(context.Background(), providerkit.RecordName{"values", "shop", "production", "cells"})
	if err != nil || len(listed) != 2 {
		t.Fatalf("List() = %v, %v, want both cells", listed, err)
	}
	for _, record := range listed {
		if record.Name.String() != one.String() && record.Name.String() != two.String() {
			t.Errorf("List() named %s, which nothing wrote", record.Name)
		}
	}
	empty, err := records.List(context.Background(), providerkit.RecordName{"values", "other", "production", "cells"})
	if err != nil || len(empty) != 0 {
		t.Errorf("List() under another project = %v, %v, want nothing", empty, err)
	}
	if _, err := records.Write(context.Background(), held); err == nil {
		t.Error("the box-side records wrote something, and a deploy writes through the helper alone")
	}
	if err := records.Remove(context.Background(), one, held.Revision); err == nil {
		t.Error("the box-side records removed something")
	}
}

func TestARecordNameRoundTripsThroughTheNameAFileAnswersTo(t *testing.T) {
	t.Parallel()
	for _, name := range []providerkit.RecordName{
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
