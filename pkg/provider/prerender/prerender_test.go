package prerender

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work above the test's directory")
		}
		dir = parent
	}
}

func nextCacheFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "frameworks", "next", "cache", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedRelease() naming.Coordinate {
	return naming.Coordinate{Project: "shop", Env: "prod", App: "web", Release: naming.NewReleaseToken("b1", "")}
}

func TestABuildsCacheAndFetchCacheFilesBecomeSeedsOfThePagesAndFetchCacheKinds(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cache", "blog.cache.json"), "12345")
	writeFile(t, filepath.Join(root, "cache", "nested", "a.cache.json"), "123")
	writeFile(t, filepath.Join(root, "fetch-cache", "abc"), "1")

	release := seedRelease()
	got, err := Seeds(root, release)
	if err != nil {
		t.Fatalf("Seeds() = %v", err)
	}

	at := "/shop/prod/web/" + release.Release.String() + "/"
	want := []Seed{
		{Key: "pages" + at + "blog.cache.json", Path: filepath.Join(root, "cache", "blog.cache.json"), Kind: naming.ObjectPages, Size: 5},
		{Key: "pages" + at + "nested/a.cache.json", Path: filepath.Join(root, "cache", "nested", "a.cache.json"), Kind: naming.ObjectPages, Size: 3},
		{Key: "fetch-cache" + at + "abc", Path: filepath.Join(root, "fetch-cache", "abc"), Kind: naming.ObjectFetchCache, Size: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Seeds() = %+v, want %+v", got, want)
	}
}

func TestAnAppWithNoPrerenderOutputHasNoSeeds(t *testing.T) {
	got, err := Seeds(t.TempDir(), seedRelease())
	if err != nil {
		t.Fatalf("Seeds() = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Seeds() = %+v, want none", got)
	}
}

func TestTheGenesisTagSnapshotMatchesTheFormatThePublishersRead(t *testing.T) {
	got, err := json.Marshal(GenesisTagSnapshot(time.UnixMilli(1750000000000)))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(nextCacheFixture(t, "genesis-tag-snapshot.json")))
	if string(got) != want {
		t.Errorf("genesis snapshot = %s, want %s", got, want)
	}
}

func TestTheTagSnapshotKeyMatchesTheEdgeContract(t *testing.T) {
	var contract struct {
		TagSnapshotKey string `json:"tagSnapshotKey"`
		TagNamespace   struct {
			StorageCoordinate string `json:"storageCoordinate"`
		} `json:"tagNamespace"`
	}
	if err := json.Unmarshal(nextCacheFixture(t, "edge-contract.json"), &contract); err != nil {
		t.Fatal(err)
	}
	release, err := naming.ParseStorageCoordinate(contract.TagNamespace.StorageCoordinate)
	if err != nil {
		t.Fatal(err)
	}
	if got := TagSnapshotKey(release); got != contract.TagSnapshotKey {
		t.Errorf("TagSnapshotKey() = %q, want %q", got, contract.TagSnapshotKey)
	}
}
