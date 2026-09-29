package image_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

func standalone(t *testing.T, dir string) workspace.Location {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return workspace.Location{Root: abs, Path: "."}
}

func chosen(t *testing.T, app image.App) image.Recipe {
	t.Helper()
	recipe, err := image.ChooseRecipe(app)
	if err != nil {
		t.Fatalf("ChooseRecipe(%+v) = %v", app, err)
	}
	return recipe
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

	recipe := chosen(t, image.App{Name: "web", Workspace: standalone(t, "testdata/dockerfileapp")})

	if want := filepath.Join(recipe.App.Dir(), image.DockerfileName); recipe.Dockerfile != want {
		t.Errorf("ChooseRecipe() built %q, want the app's own %s", recipe.Dockerfile, want)
	}
	notice := recipe.Notice()
	if notice == "" {
		t.Fatal("the switch to a Dockerfile is announced by nothing, so a file dropped beside an app silently changes how it is built")
	}
	if strings.Count(notice, "\n") != 0 {
		t.Errorf("the notice is %q, want one line", notice)
	}
	for _, want := range []string{"web", image.DockerfileName} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice is %q, and never names %s", notice, want)
		}
	}
}

func TestADockerfileInAWorkspaceIsToldWhereItsCopiesAreReadFrom(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "apps", "web")
	write(t, filepath.Join(appDir, image.DockerfileName))
	member := workspace.Location{Root: root, Path: "apps/web", Member: true, PackageManager: workspace.Pnpm}

	notice := chosen(t, image.App{Name: "web", Workspace: member}).Notice()

	if strings.Count(notice, "\n") != 0 {
		t.Errorf("the notice is %q, want one line", notice)
	}
	for _, want := range []string{"web", image.DockerfileName, root} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice is %q, and never names %s — a COPY package.json . now reads the workspace root, and nothing else says so", notice, want)
		}
	}
}

func TestAnAppWithNoDockerfileIsBuiltByRailpackAndAnnouncesNothing(t *testing.T) {
	t.Parallel()

	recipe := chosen(t, image.App{Name: "web", Workspace: standalone(t, "testdata/plainserver")})

	if recipe.Dockerfile != "" {
		t.Errorf("ChooseRecipe() built from %q, want railpack where the app has no Dockerfile", recipe.Dockerfile)
	}
	if notice := recipe.Notice(); notice != "" {
		t.Errorf("the default build announces %q, want nothing said about the builder nobody switched", notice)
	}
}

func TestRemovingTheDockerfileIsTheWayBackToRailpack(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, image.DockerfileName))
	if chosen(t, image.App{Name: "web", Workspace: standalone(t, dir)}).Dockerfile == "" {
		t.Fatal("ChooseRecipe() ignored a Dockerfile beside the app")
	}

	if err := os.Rename(filepath.Join(dir, image.DockerfileName), filepath.Join(dir, "Dockerfile.unused")); err != nil {
		t.Fatal(err)
	}

	if got := chosen(t, image.App{Name: "web", Workspace: standalone(t, dir)}).Dockerfile; got != "" {
		t.Errorf("ChooseRecipe() still builds from %q after the Dockerfile was renamed, so there is no way back to railpack", got)
	}
}

func TestOnlyTheExactNameDockerfileSwitchesAnything(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"dockerfile",
		"DOCKERFILE",
		"Dockerfile.dev",
		"dockerfile.prod",
		"docker/Dockerfile",
		"src/Dockerfile",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			write(t, filepath.Join(dir, filepath.FromSlash(name)))

			recipe := chosen(t, image.App{Name: "web", Workspace: standalone(t, dir)})

			if recipe.Dockerfile != "" {
				t.Errorf("%s switched the build to %q, and only a file named exactly %s in the app's own directory does that", name, recipe.Dockerfile, image.DockerfileName)
			}
			if notice := recipe.Notice(); notice != "" {
				t.Errorf("%s announced %q, having switched nothing", name, notice)
			}
		})
	}
}

func TestADirectoryNamedDockerfileSwitchesNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, image.DockerfileName), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := chosen(t, image.App{Name: "web", Workspace: standalone(t, dir)}).Dockerfile; got != "" {
		t.Errorf("ChooseRecipe() built from %q, and a directory is not a Dockerfile", got)
	}
}

func TestASymlinkNamedDockerfileIsFollowedToWhatItPointsAt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "api")
	write(t, filepath.Join(root, "shared", image.DockerfileName))
	if err := os.MkdirAll(filepath.Join(appDir, "linking"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("to a directory", func(t *testing.T) {
		linked := filepath.Join(appDir, "linking", image.DockerfileName)
		if err := os.Symlink(filepath.Join(root, "shared"), linked); err != nil {
			t.Skipf("this machine makes no symlinks: %v", err)
		}

		if got := chosen(t, image.App{Name: "web", Workspace: standalone(t, filepath.Join(appDir, "linking"))}).Dockerfile; got != "" {
			t.Errorf("ChooseRecipe() built from %q, and a link to a directory is no more a Dockerfile than the directory is — buildkit finds that out with an error nobody can read", got)
		}
	})

	t.Run("to a file", func(t *testing.T) {
		linked := filepath.Join(appDir, image.DockerfileName)
		if err := os.Symlink(filepath.Join(root, "shared", image.DockerfileName), linked); err != nil {
			t.Skipf("this machine makes no symlinks: %v", err)
		}

		if got := chosen(t, image.App{Name: "web", Workspace: standalone(t, appDir)}).Dockerfile; got != linked {
			t.Errorf("ChooseRecipe() built from %q, want %q — a link to a Dockerfile builds what it points at", got, linked)
		}
	})
}

func TestAConfiguredDockerfileMayLiveOutsideTheAppItBuilds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "services", "api")
	shared := filepath.Join(root, "shared", image.DockerfileName)
	write(t, filepath.Join(appDir, "package.json"))
	write(t, shared)

	recipe := chosen(t, image.App{Name: "api", Workspace: standalone(t, appDir), Dockerfile: "../../shared/Dockerfile"})

	if recipe.Dockerfile != shared {
		t.Errorf("ChooseRecipe() built from %q, want the %q the app's build names, resolved against the app's directory", recipe.Dockerfile, shared)
	}
	if recipe.App.Dir() != appDir {
		t.Errorf("the build context is %q, want the app's own directory %q wherever its Dockerfile lives", recipe.App.Dir(), appDir)
	}
	if notice := recipe.Notice(); !strings.Contains(notice, "api") || !strings.Contains(notice, shared) {
		t.Errorf("the notice is %q, want it to name the app and the Dockerfile it was pointed at", notice)
	}
}

func TestAConfiguredDockerfileBeatsTheOneBesideTheApp(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "api")
	write(t, filepath.Join(appDir, image.DockerfileName))
	write(t, filepath.Join(root, "shared", image.DockerfileName))

	recipe := chosen(t, image.App{Name: "api", Workspace: standalone(t, appDir), Dockerfile: "../shared/Dockerfile"})

	if want := filepath.Join(root, "shared", image.DockerfileName); recipe.Dockerfile != want {
		t.Errorf("ChooseRecipe() built from %q, want the %q the app asked for", recipe.Dockerfile, want)
	}
}

func TestAConfiguredDockerfileThatIsNotThereRefusesTheBuildByName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := image.ChooseRecipe(image.App{Name: "api", Workspace: standalone(t, dir), Dockerfile: "build/Dockerfile"})
	if err == nil {
		t.Fatal("ChooseRecipe() accepted a build.dockerfile naming nothing, so the deploy would reach the solve before finding out")
	}
	for _, want := range []string{"api", "build/Dockerfile", "build.dockerfile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ChooseRecipe() = %v, and the reason never names %s", err, want)
		}
	}
}

func TestAnAppDirectoryThatIsNotThereRefusesTheBuildByName(t *testing.T) {
	t.Parallel()

	_, err := image.ChooseRecipe(image.App{Name: "api", Workspace: standalone(t, filepath.Join(t.TempDir(), "absent"))})
	if err == nil {
		t.Fatal("ChooseRecipe() read a directory that does not exist as an app with no Dockerfile")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("ChooseRecipe() = %v, and the reason never names the app", err)
	}
}
