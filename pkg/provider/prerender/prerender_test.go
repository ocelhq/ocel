package prerender

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestABuildsCacheAndFetchCacheFilesBecomeSeedsUnderTheAppsISRPrefix(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cache", "blog.cache.json"), "12345")
	writeFile(t, filepath.Join(root, "cache", "nested", "a.cache.json"), "123")
	writeFile(t, filepath.Join(root, "fetch-cache", "abc"), "1")

	got, err := Seeds(root, "p")
	if err != nil {
		t.Fatalf("Seeds() = %v", err)
	}

	want := []Seed{
		{Key: "p/cache/blog.cache.json", Path: filepath.Join(root, "cache", "blog.cache.json"), Segment: EntrySegment, Size: 5},
		{Key: "p/cache/nested/a.cache.json", Path: filepath.Join(root, "cache", "nested", "a.cache.json"), Segment: EntrySegment, Size: 3},
		{Key: "p/fetch-cache/abc", Path: filepath.Join(root, "fetch-cache", "abc"), Segment: FetchSegment, Size: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Seeds() = %+v, want %+v", got, want)
	}
}

func TestAnAppWithNoPrerenderOutputHasNoSeeds(t *testing.T) {
	got, err := Seeds(t.TempDir(), "p")
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
		TagSnapshotSuffix string `json:"tagSnapshotSuffix"`
	}
	if err := json.Unmarshal(nextCacheFixture(t, "edge-contract.json"), &contract); err != nil {
		t.Fatal(err)
	}
	if got, want := TagSnapshotKey("x"), "x"+contract.TagSnapshotSuffix; got != want {
		t.Errorf("TagSnapshotKey(%q) = %q, want %q", "x", got, want)
	}
}
