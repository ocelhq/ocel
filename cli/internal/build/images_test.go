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
		image: func(_ context.Context, app image.App, arch string, _ image.LiveValues, _ io.Writer) (image.Image, error) {
			asked = arch
			return image.Image{Ref: "ocel/shop/" + app.Name + "@sha256:0"}, nil
		},
	}.apps(context.Background(), cfg, nil, map[string]string{"web": "arm64"}, nil, Host{}, Log{})
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
		image: func(_ context.Context, app image.App, _ string, _ image.LiveValues, _ io.Writer) (image.Image, error) {
			return image.Image{Ref: "ocel/shop/" + app.Name + "@sha256:" + strings.Repeat("a", 64)}, nil
		},
	}.apps(context.Background(), cfg, nil, nil, nil, Host{}, Log{})
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

func TestAContainerAppIsBuiltWithTheBindingsAndSecretsOfItsOwnAppAlone(t *testing.T) {
	cfg := containerProject(t, "")
	cfg.Apps = append(cfg.Apps, project.App{Name: "api", Path: "services/web", Compute: "container"})
	got := map[string]map[string]string{}
	_, err := tools{
		image: func(_ context.Context, app image.App, _ string, live image.LiveValues, _ io.Writer) (image.Image, error) {
			got[app.Name] = live.Values
			return image.Image{Ref: "ocel/shop/" + app.Name + "@sha256:0"}, nil
		},
		liveHashKey: func() ([]byte, error) { return []byte("machine key"), nil },
	}.apps(context.Background(), cfg, map[string]AppVariables{
		"web": {Env: map[string]string{"PLAIN": "p"}, Live: map[string]string{"OCEL_BINDING_DB": "db-web"}},
		"api": {Live: map[string]string{"OCEL_BINDING_KV": "kv-api"}},
	}, nil, nil, Host{}, Log{})
	if err != nil {
		t.Fatalf("apps() = %v", err)
	}

	if want := map[string]string{"OCEL_BINDING_DB": "db-web"}; !maps.Equal(got["web"], want) {
		t.Errorf("web was built with %v, want %v: the plain variables are not secrets, and another app's are not its own", got["web"], want)
	}
	if want := map[string]string{"OCEL_BINDING_KV": "kv-api"}; !maps.Equal(got["api"], want) {
		t.Errorf("api was built with %v, want %v", got["api"], want)
	}
}

func TestNoValueAContainerBuildWasGivenReachesTheLogOrTheError(t *testing.T) {
	const value = "postgres://u:hunter2@127.0.0.1:5432/db"
	cfg := containerProject(t, "")
	var logged strings.Builder
	var ended error
	log := Log{AppLog: func(string) (io.Writer, func(error)) { return &logged, func(err error) { ended = err } }}

	_, err := tools{
		image: func(_ context.Context, _ image.App, _ string, _ image.LiveValues, progress io.Writer) (image.Image, error) {
			_, _ = io.WriteString(progress, "connecting to "+value+"\n")
			return image.Image{}, errors.New("build web: could not reach " + value)
		},
		liveHashKey: func() ([]byte, error) { return []byte("machine key"), nil },
	}.apps(context.Background(), cfg, map[string]AppVariables{
		"web": {Live: map[string]string{"OCEL_BINDING_DB": value}},
	}, nil, nil, Host{}, log)

	if err == nil {
		t.Fatal("apps() succeeded though the image build failed")
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(logged.String(), "hunter2") {
		t.Errorf("a value the build was given reached its output:\nlog: %s\nerror: %v", logged.String(), err)
	}
	if ended == nil || strings.Contains(ended.Error(), "hunter2") {
		t.Errorf("the app's log was ended with %v, want the failure with the value hidden, since the terminal prints it as the app ends", ended)
	}
}

func TestTheLiveValuesOfAContainerBuildAreHashedWithTheMachinesKey(t *testing.T) {
	values := map[string]string{"OCEL_BINDING_DB": "db-web"}
	hashed := map[string]string{}
	for _, key := range []string{"one machine", "another machine"} {
		_, err := tools{
			image: func(_ context.Context, app image.App, _ string, live image.LiveValues, _ io.Writer) (image.Image, error) {
				hashed[key] = live.Hash
				return image.Image{Ref: "ocel/shop/" + app.Name + "@sha256:0"}, nil
			},
			liveHashKey: func() ([]byte, error) { return []byte(key), nil },
		}.apps(context.Background(), containerProject(t, ""), map[string]AppVariables{"web": {Live: values}}, nil, nil, Host{}, Log{})
		if err != nil {
			t.Fatalf("apps() = %v", err)
		}
	}

	if hashed["one machine"] == "" || hashed["one machine"] == hashed["another machine"] {
		t.Errorf("the same values hashed to %q on one machine and %q on another, want two different hashes, so a hash names no value without the key", hashed["one machine"], hashed["another machine"])
	}
}

func TestAContainerBuildWithNoLiveValuesNeverReadsTheMachinesKey(t *testing.T) {
	_, err := tools{
		image: func(_ context.Context, app image.App, _ string, _ image.LiveValues, _ io.Writer) (image.Image, error) {
			return image.Image{Ref: "ocel/shop/" + app.Name + "@sha256:0"}, nil
		},
		liveHashKey: func() ([]byte, error) {
			t.Error("the build read the key though it hashes nothing")
			return nil, nil
		},
	}.apps(context.Background(), containerProject(t, ""), nil, nil, nil, Host{}, Log{})
	if err != nil {
		t.Fatalf("apps() = %v", err)
	}
}
