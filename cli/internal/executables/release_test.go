package executables

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheLockPinsWhatGoreleaserActuallyBuilt(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(root, "dist", ChecksumsAsset))
	if err != nil {
		t.Skip("no dist/checksums.txt; run `goreleaser release --snapshot --skip=publish`")
	}
	defer file.Close()

	sums, err := ParseChecksums(file)
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}

	var version string
	for asset := range sums {
		if parsed, ok := ParseAssetName(asset); ok {
			version = parsed.Version
			break
		}
	}
	if version == "" {
		t.Fatal("the release contains no archive the fetcher can name")
	}

	lock := lockFromChecksums(version, sums)
	if len(lock.Providers) == 0 {
		t.Fatal("the release contains no provider the lock can pin")
	}
	if len(lock.Connectors) == 0 {
		t.Fatal("the release contains no connector the lock can pin")
	}
	for name, pinned := range pinsOfKind(lock, KindProvider) {
		for _, platform := range Platforms {
			if _, ok := pinned[platform.Dir()]; !ok {
				t.Errorf("the release ships no %s provider for %s, a platform the CLI runs on", name, platform.Dir())
			}
		}
	}
	for name, pinned := range pinsOfKind(lock, KindConnector) {
		if len(pinned) == 0 {
			t.Errorf("the lock pins the %s connector for no platform", name)
		}
	}
}
