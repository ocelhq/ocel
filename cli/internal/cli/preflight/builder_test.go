package preflight

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func awayFromAnyDaemon(t *testing.T) {
	t.Helper()
	t.Setenv(imagebuild.DockerHostEnv, "unix://"+filepath.Join(t.TempDir(), "absent.sock"))
}

func TestAContainerAppWithNoDaemonToBuildItIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "api", Compute: "serverless"},
		{Name: "web", Compute: "container"},
	}}

	err := RequireBuilder(context.Background(), cfg)
	if err == nil {
		t.Fatal("RequireBuilder() with no daemon reachable succeeded, so a container deploy would provision before discovering it cannot build")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("RequireBuilder() = %v, and the reader never learns which app needs a daemon", err)
	}
	if !strings.Contains(err.Error(), imagebuild.DockerHostEnv) {
		t.Errorf("RequireBuilder() = %v, and the reader is never told which variable points ocel at a daemon", err)
	}
}

func TestAProjectOfServerlessAppsNeverAsksForADaemon(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "api", Compute: "serverless"},
		{Name: "web", Compute: "serverless"},
	}}

	if err := RequireBuilder(context.Background(), cfg); err != nil {
		t.Errorf("RequireBuilder() over a project with no container app = %v, want a deploy that never needed docker to be unaffected by its absence", err)
	}
}
