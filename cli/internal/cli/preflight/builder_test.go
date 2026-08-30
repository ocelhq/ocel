package preflight

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
)

func awayFromAnyDaemon(t *testing.T) {
	t.Helper()
	t.Setenv(imagebuild.DockerHostEnv, "unix://"+filepath.Join(t.TempDir(), "absent.sock"))
}

func TestAContainerAppWithNoDaemonToBuildItIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := containerProject(t, "")
	cfg.Apps = append([]projectconfig.App{{Name: "api", Compute: "serverless"}}, cfg.Apps...)
	rep, _ := said(t)

	err := RequireBuilder(context.Background(), rep, cfg)
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

	rep, _ := said(t)

	if err := RequireBuilder(context.Background(), rep, cfg); err != nil {
		t.Errorf("RequireBuilder() over a project with no container app = %v, want a deploy that never needed docker to be unaffected by its absence", err)
	}
}

func containerProject(t *testing.T, dockerfile string) *projectconfig.Config {
	t.Helper()
	root := t.TempDir()
	appDir := filepath.Join(root, "services", "web")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if dockerfile != "" {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(dockerfile)), []byte("FROM scratch\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "web", Path: "services/web", Compute: "container"}},
	}
}

func said(t *testing.T) (runui.Reporter, *bytes.Buffer) {
	t.Helper()
	var b bytes.Buffer
	return runui.Plain(runui.Presentation{}, &b), &b
}

func TestTheDeployAnnouncesTheDockerfileAnAppSwitchedItselfTo(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := containerProject(t, "services/web/Dockerfile")
	rep, out := said(t)

	if err := RequireBuilder(context.Background(), rep, cfg); err == nil {
		t.Fatal("RequireBuilder() with no daemon reachable succeeded")
	}

	notice := out.String()
	if !strings.Contains(notice, "web") || !strings.Contains(notice, imagebuild.DockerfileName) {
		t.Errorf("the deploy said %q, and never announced that a Dockerfile changed how %q is built", notice, "web")
	}
}

func TestAContainerAppRailpackBuildsAnnouncesNothing(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := containerProject(t, "")
	rep, out := said(t)

	if err := RequireBuilder(context.Background(), rep, cfg); err == nil {
		t.Fatal("RequireBuilder() with no daemon reachable succeeded")
	}

	if notice := out.String(); notice != "" {
		t.Errorf("the deploy said %q about a build nobody switched", notice)
	}
}

func TestABuildDockerfileNamingNothingStopsTheDeployBeforeTheDaemonIsAsked(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := containerProject(t, "")
	cfg.Apps[0].Build = &projectconfig.Build{Dockerfile: "../shared/Dockerfile"}
	rep, _ := said(t)

	err := RequireBuilder(context.Background(), rep, cfg)
	if err == nil {
		t.Fatal("RequireBuilder() accepted a build.dockerfile naming nothing")
	}
	if !strings.Contains(err.Error(), "build.dockerfile") || !strings.Contains(err.Error(), "web") {
		t.Errorf("RequireBuilder() = %v, want the app and the key it got wrong named", err)
	}
	if strings.Contains(err.Error(), imagebuild.DockerHostEnv) {
		t.Errorf("RequireBuilder() = %v, and a config the deploy can never act on is reported as a docker problem", err)
	}
}
