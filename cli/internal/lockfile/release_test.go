package lockfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/providers"
)

func TestTheLockPinsWhatGoreleaserActuallyBuilt(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(root, "dist", providers.ChecksumsAsset))
	if err != nil {
		t.Skip("no dist/checksums.txt; run `goreleaser release --snapshot --skip=publish`")
	}
	defer file.Close()

	sums, err := providers.ParseChecksums(file)
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}

	var version string
	for asset := range sums {
		if parsed, ok := providers.ParseAssetName(asset); ok {
			version = parsed.Version
			break
		}
	}
	if version == "" {
		t.Fatal("the release holds no archive the fetcher can name")
	}

	lock := FromChecksums(version, sums)
	if len(lock.Providers) == 0 {
		t.Fatal("the release holds no provider the lock can pin")
	}
	for name, pinned := range lock.Providers {
		for _, platform := range providers.Platforms {
			if _, held := pinned[platform.Dir()]; !held {
				t.Errorf("the release ships no %s provider for %s", name, platform.Dir())
			}
		}
	}
}
