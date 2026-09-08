package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestADetectedAppIsReadInTheLanguageOfTheProjectItSitsIn(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/web\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	cfg := &projectconfig.Config{Dir: root}
	apps, err := toAttributionApps(cfg, []manifestbuilder.Function{{App: "web"}}, "", "ocel.config.ts")
	if err != nil {
		t.Fatalf("toAttributionApps: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("toAttributionApps returned %d apps, want 1", len(apps))
	}
	if apps[0].Language != discovery.Go {
		t.Errorf("Language = %q, want %q", apps[0].Language, discovery.Go)
	}
}
