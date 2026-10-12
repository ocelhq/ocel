package gcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestTheRouteTableHoldingABypassTokenIsNotPublishedWithTheStaticFiles(t *testing.T) {
	t.Parallel()

	appRoot := t.TempDir()
	for name, body := range map[string]string{
		filepath.Join(edge.StaticAssetDir, "logo.svg"): "<svg/>",
		edge.OcelRouteTableFile:                        `{"bypassToken":"secret"}`,
		"hosting.json":                                 "{}",
	} {
		path := filepath.Join(appRoot, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := newStaticKeys("production/shop/web/r1a2b3c4d/assets")
	if err != nil {
		t.Fatal(err)
	}

	files, err := staticFilesOf(appRoot, keys)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 || files[0].key != "production/shop/web/r1a2b3c4d/assets/logo.svg" {
		t.Errorf("published %+v, want the one file under %s and nothing beside it", files, edge.StaticAssetDir)
	}
}
