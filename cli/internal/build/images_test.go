package build

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/images"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func awayFromAnyDaemon(t *testing.T) {
	t.Helper()
	t.Setenv(images.DockerHostEnv, "unix://"+filepath.Join(t.TempDir(), "absent.sock"))
}

func TestAContainerAppWithNoDaemonToBuildItIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := containerProject(t, "")
	cfg.Apps = append([]project.App{{Name: "api", Compute: "serverless"}}, cfg.Apps...)
	rep, _ := said(t)

	err := RefuseUnbuildableImages(context.Background(), rep, cfg, nil)
	if err == nil {
		t.Fatal("RefuseUnbuildableImages() with no daemon reachable succeeded, so a container deploy would provision before discovering it cannot build")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("RefuseUnbuildableImages() = %v, and the reader never learns which app needs a daemon", err)
	}
	if !strings.Contains(err.Error(), images.DockerHostEnv) {
		t.Errorf("RefuseUnbuildableImages() = %v, and the reader is never told which variable points ocel at a daemon", err)
	}
}

func TestAProjectOfServerlessAppsNeverAsksForADaemon(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := &project.Project{Apps: []project.App{
		{Name: "api", Compute: "serverless"},
		{Name: "web", Compute: "serverless"},
	}}

	rep, _ := said(t)

	if err := RefuseUnbuildableImages(context.Background(), rep, cfg, nil); err != nil {
		t.Errorf("RefuseUnbuildableImages() over a project with no container app = %v, want a deploy that never needed docker to be unaffected by its absence", err)
	}
}

func containerProject(t *testing.T, dockerfile string) *project.Project {
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
	return &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "web", Path: "services/web", Compute: "container"}},
	}
}

type heard struct {
	mu   sync.Mutex
	said strings.Builder
}

func (h *heard) Receive(ev *streamv1.RunEvent) {
	if ev.GetOperation().GetBody() != nil || ev.GetCli() != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.said.WriteString(ev.GetOperation().GetMessage() + "\n")
}

func (h *heard) Close() error { return nil }

func (h *heard) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.said.String()
}

func said(t *testing.T) (*run.Span, *heard) {
	t.Helper()
	h := &heard{}
	bus := run.NewBus(time.Now)
	bus.Attach(h)
	_, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	return run.Phase(progressv1.Phase_PHASE_CHECK), h
}

func TestTheDeployAnnouncesTheDockerfileAnAppSwitchedItselfTo(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := containerProject(t, "services/web/Dockerfile")
	rep, out := said(t)

	if err := RefuseUnbuildableImages(context.Background(), rep, cfg, nil); err == nil {
		t.Fatal("RefuseUnbuildableImages() with no daemon reachable succeeded")
	}

	notice := out.String()
	if !strings.Contains(notice, "web") || !strings.Contains(notice, image.DockerfileName) {
		t.Errorf("the deploy said %q, and never announced that a Dockerfile changed how %q is built", notice, "web")
	}
}

func TestAContainerAppRailpackBuildsAnnouncesNothing(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := containerProject(t, "")
	rep, out := said(t)

	if err := RefuseUnbuildableImages(context.Background(), rep, cfg, nil); err == nil {
		t.Fatal("RefuseUnbuildableImages() with no daemon reachable succeeded")
	}

	if notice := out.String(); notice != "" {
		t.Errorf("the deploy said %q about a build nobody switched", notice)
	}
}

func TestABuildDockerfileNamingNothingStopsTheDeployBeforeTheDaemonIsAsked(t *testing.T) {
	awayFromAnyDaemon(t)
	cfg := containerProject(t, "")
	cfg.Apps[0].Container = &project.Container{Build: &project.Build{Dockerfile: "../shared/Dockerfile"}}
	rep, _ := said(t)

	err := RefuseUnbuildableImages(context.Background(), rep, cfg, nil)
	if err == nil {
		t.Fatal("RefuseUnbuildableImages() accepted a build.dockerfile naming nothing")
	}
	if !strings.Contains(err.Error(), "build.dockerfile") || !strings.Contains(err.Error(), "web") {
		t.Errorf("RefuseUnbuildableImages() = %v, want the app and the key it got wrong named", err)
	}
	if strings.Contains(err.Error(), images.DockerHostEnv) {
		t.Errorf("RefuseUnbuildableImages() = %v, and a config the deploy can never act on is reported as a docker problem", err)
	}
}

func TestAContainerAppIsBuiltIntoAnImageForTheArchitectureItIsAskedAndNeverHandedToTheNodeBuilder(t *testing.T) {
	cfg := containerProject(t, "")
	cfg.Slug = "shop"
	var asked string
	built, err := tools{
		node: func(context.Context, string, []byte, Log) error {
			t.Error("the node builder ran for a project whose only app runs in an image")
			return nil
		},
		image: func(_ context.Context, app image.App, arch string, _ io.Writer) (image.Image, error) {
			asked = arch
			return image.Image{Ref: "ocel/shop/" + app.Name + "@sha256:0"}, nil
		},
	}.apps(context.Background(), cfg, nil, map[string]string{"web": "arm64"}, nil, Log{})
	if err != nil {
		t.Fatalf("apps() = %v", err)
	}
	if asked != "arm64" {
		t.Errorf("the image was built for %q, want the %q it was asked for", asked, "arm64")
	}
	if got := built.Images["web"]; got != "ocel/shop/web@sha256:0" {
		t.Errorf("the build reported the image %q for web, want the one it built", got)
	}
	if len(built.Functions) != 0 {
		t.Errorf("the build reported functions %+v for a project whose only app runs in an image", built.Functions)
	}
}

func builtImage(t *testing.T, cfg *project.Project) Output {
	t.Helper()
	built, err := tools{
		image: func(_ context.Context, app image.App, _ string, _ io.Writer) (image.Image, error) {
			return image.Image{Ref: "ocel/shop/" + app.Name + "@sha256:" + strings.Repeat("a", 64)}, nil
		},
	}.apps(context.Background(), cfg, nil, nil, nil, Log{})
	if err != nil {
		t.Fatalf("apps() = %v", err)
	}
	return built
}

func daemonHolding(architecture string) func(context.Context, string, string) (string, error) {
	return func(context.Context, string, string) (string, error) { return architecture, nil }
}

func TestAPrebuiltImageIsReadBackRatherThanBuiltAgain(t *testing.T) {
	cfg := containerProject(t, "")
	built := builtImage(t, cfg)

	prebuilt, err := tools{architecture: daemonHolding("arm64")}.readPrebuilt(context.Background(), cfg, map[string]string{"web": "arm64"})
	if err != nil {
		t.Fatalf("readPrebuilt() = %v", err)
	}
	if !maps.Equal(prebuilt.Images, built.Images) {
		t.Errorf("--prebuilt read the images %v, want the %v the build recorded", prebuilt.Images, built.Images)
	}
}

func TestAPrebuiltContainerAppWithNoImageRecordedIsRefusedByName(t *testing.T) {
	cfg := containerProject(t, "")
	if err := os.MkdirAll(outputRoot(t, cfg.Dir), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := tools{architecture: daemonHolding("amd64")}.readPrebuilt(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("readPrebuilt() deployed a container app the prebuilt output holds no image for")
	}
	for _, want := range []string{"web", "ocel build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("readPrebuilt() = %v, want it to name %q", err, want)
		}
	}
}

func TestAPrebuiltImageTheDaemonNoLongerHoldsIsRefused(t *testing.T) {
	cfg := containerProject(t, "")
	builtImage(t, cfg)

	gone := func(context.Context, string, string) (string, error) {
		return "", errors.New("the daemon answered \"404 Not Found\"")
	}
	_, err := tools{architecture: gone}.readPrebuilt(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("readPrebuilt() accepted an image the daemon no longer holds, so the push fails after the deploy has started")
	}
	if !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "404") {
		t.Errorf("readPrebuilt() = %v, want the app and what the daemon said", err)
	}
}

func TestAPrebuiltImageBuiltForAnotherArchitectureThanTheTargetRunsIsRefused(t *testing.T) {
	cfg := containerProject(t, "")
	builtImage(t, cfg)

	_, err := tools{architecture: daemonHolding("amd64")}.readPrebuilt(context.Background(), cfg, map[string]string{"web": "arm64"})
	if err == nil {
		t.Fatal("readPrebuilt() accepted an amd64 image for a target that runs arm64")
	}
	for _, want := range []string{"web", "amd64", "arm64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("readPrebuilt() = %v, want it to name %q", err, want)
		}
	}
}
