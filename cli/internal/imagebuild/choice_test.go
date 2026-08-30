package imagebuild_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
)

func chosen(t *testing.T, app imagebuild.App) imagebuild.Choice {
	t.Helper()
	choice, err := imagebuild.Choose(app)
	if err != nil {
		t.Fatalf("Choose(%+v) = %v", app, err)
	}
	return choice
}

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestADockerfileBesideAnAppSwitchesTheBuildToIt(t *testing.T) {
	t.Parallel()

	choice := chosen(t, imagebuild.App{Name: "web", Dir: "testdata/dockerfileapp"})

	if want := filepath.Join("testdata/dockerfileapp", imagebuild.DockerfileName); choice.Dockerfile != want {
		t.Errorf("Choose() built %q, want the app's own %s", choice.Dockerfile, want)
	}
	notice := choice.Notice()
	if notice == "" {
		t.Fatal("the switch to a Dockerfile is announced by nothing, so a file dropped beside an app silently changes how it is built")
	}
	if strings.Count(notice, "\n") != 0 {
		t.Errorf("the notice is %q, want one line", notice)
	}
	for _, want := range []string{"web", imagebuild.DockerfileName} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice is %q, and never names %s", notice, want)
		}
	}
}

func TestAnAppWithNoDockerfileIsBuiltByRailpackAndAnnouncesNothing(t *testing.T) {
	t.Parallel()

	choice := chosen(t, imagebuild.App{Name: "web", Dir: "testdata/plainserver"})

	if choice.Dockerfile != "" {
		t.Errorf("Choose() built from %q, want railpack where the app has no Dockerfile", choice.Dockerfile)
	}
	if notice := choice.Notice(); notice != "" {
		t.Errorf("the default build announces %q, want nothing said about the builder nobody switched", notice)
	}
}

func TestRemovingTheDockerfileIsTheWayBackToRailpack(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, imagebuild.DockerfileName))
	if chosen(t, imagebuild.App{Name: "web", Dir: dir}).Dockerfile == "" {
		t.Fatal("Choose() ignored a Dockerfile beside the app")
	}

	if err := os.Rename(filepath.Join(dir, imagebuild.DockerfileName), filepath.Join(dir, "Dockerfile.unused")); err != nil {
		t.Fatal(err)
	}

	if got := chosen(t, imagebuild.App{Name: "web", Dir: dir}).Dockerfile; got != "" {
		t.Errorf("Choose() still builds from %q after the Dockerfile was renamed, so there is no way back to railpack", got)
	}
}

func TestOnlyTheExactNameDockerfileSwitchesAnything(t *testing.T) {
	t.Parallel()

	for _, held := range []string{
		"dockerfile",
		"DOCKERFILE",
		"Dockerfile.dev",
		"dockerfile.prod",
		"docker/Dockerfile",
		"src/Dockerfile",
	} {
		t.Run(held, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			write(t, filepath.Join(dir, filepath.FromSlash(held)))

			choice := chosen(t, imagebuild.App{Name: "web", Dir: dir})

			if choice.Dockerfile != "" {
				t.Errorf("%s switched the build to %q, and only a file named exactly %s in the app's own directory does that", held, choice.Dockerfile, imagebuild.DockerfileName)
			}
			if notice := choice.Notice(); notice != "" {
				t.Errorf("%s announced %q, having switched nothing", held, notice)
			}
		})
	}
}

func TestADirectoryNamedDockerfileSwitchesNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, imagebuild.DockerfileName), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := chosen(t, imagebuild.App{Name: "web", Dir: dir}).Dockerfile; got != "" {
		t.Errorf("Choose() built from %q, and a directory is not a Dockerfile", got)
	}
}

func TestASymlinkNamedDockerfileIsFollowedToWhatItPointsAt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "api")
	write(t, filepath.Join(root, "shared", imagebuild.DockerfileName))
	if err := os.MkdirAll(filepath.Join(appDir, "holder"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("to a directory", func(t *testing.T) {
		linked := filepath.Join(appDir, "holder", imagebuild.DockerfileName)
		if err := os.Symlink(filepath.Join(root, "shared"), linked); err != nil {
			t.Skipf("this machine makes no symlinks: %v", err)
		}

		if got := chosen(t, imagebuild.App{Name: "web", Dir: filepath.Join(appDir, "holder")}).Dockerfile; got != "" {
			t.Errorf("Choose() built from %q, and a link to a directory is no more a Dockerfile than the directory is — buildkit finds that out with an error nobody can read", got)
		}
	})

	t.Run("to a file", func(t *testing.T) {
		linked := filepath.Join(appDir, imagebuild.DockerfileName)
		if err := os.Symlink(filepath.Join(root, "shared", imagebuild.DockerfileName), linked); err != nil {
			t.Skipf("this machine makes no symlinks: %v", err)
		}

		if got := chosen(t, imagebuild.App{Name: "web", Dir: appDir}).Dockerfile; got != linked {
			t.Errorf("Choose() built from %q, want %q — a link to a Dockerfile builds what it points at", got, linked)
		}
	})
}

func TestAConfiguredDockerfileMayLiveOutsideTheAppItBuilds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "services", "api")
	shared := filepath.Join(root, "shared", imagebuild.DockerfileName)
	write(t, filepath.Join(appDir, "package.json"))
	write(t, shared)

	choice := chosen(t, imagebuild.App{Name: "api", Dir: appDir, Configured: "../../shared/Dockerfile"})

	if choice.Dockerfile != shared {
		t.Errorf("Choose() built from %q, want the %q the app's build names, resolved against the app's directory", choice.Dockerfile, shared)
	}
	if choice.App.Dir != appDir {
		t.Errorf("the build context is %q, want the app's own directory %q wherever its Dockerfile lives", choice.App.Dir, appDir)
	}
	if notice := choice.Notice(); !strings.Contains(notice, "api") || !strings.Contains(notice, shared) {
		t.Errorf("the notice is %q, want it to name the app and the Dockerfile it was pointed at", notice)
	}
}

func TestAConfiguredDockerfileBeatsTheOneBesideTheApp(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "api")
	write(t, filepath.Join(appDir, imagebuild.DockerfileName))
	write(t, filepath.Join(root, "shared", imagebuild.DockerfileName))

	choice := chosen(t, imagebuild.App{Name: "api", Dir: appDir, Configured: "../shared/Dockerfile"})

	if want := filepath.Join(root, "shared", imagebuild.DockerfileName); choice.Dockerfile != want {
		t.Errorf("Choose() built from %q, want the %q the app asked for", choice.Dockerfile, want)
	}
}

func TestAConfiguredDockerfileThatIsNotThereRefusesTheBuildByName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := imagebuild.Choose(imagebuild.App{Name: "api", Dir: dir, Configured: "build/Dockerfile"})
	if err == nil {
		t.Fatal("Choose() accepted a build.dockerfile naming nothing, so the deploy would reach the solve before finding out")
	}
	for _, want := range []string{"api", "build/Dockerfile", "build.dockerfile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Choose() = %v, and the reason never names %s", err, want)
		}
	}
}

func TestAnAppDirectoryThatIsNotThereRefusesTheBuildByName(t *testing.T) {
	t.Parallel()

	_, err := imagebuild.Choose(imagebuild.App{Name: "api", Dir: filepath.Join(t.TempDir(), "absent")})
	if err == nil {
		t.Fatal("Choose() read a directory that does not exist as an app with no Dockerfile")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("Choose() = %v, and the reason never names the app", err)
	}
}
