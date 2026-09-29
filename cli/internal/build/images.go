package build

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/english"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/images"
)

func (t tools) images(ctx context.Context, cfg *projectconfig.Config, archs map[string]string, log Log) (map[string]string, error) {
	var refs map[string]string
	for _, app := range ImageApps(cfg.Apps) {
		described, err := image.Describe(cfg, app)
		if err != nil {
			return nil, err
		}
		appLog, ended := log.App(app.Name)
		built, err := t.image(ctx, described, archs[app.Name], appLog)
		ended(err)
		if err != nil {
			return nil, err
		}
		if err := writeImageRef(cfg.Dir, app.Name, built.Ref); err != nil {
			return nil, err
		}
		if refs == nil {
			refs = map[string]string{}
		}
		refs[app.Name] = built.Ref
	}
	return refs, nil
}

func (t tools) readPrebuilt(ctx context.Context, cfg *projectconfig.Config, archs map[string]string) (Output, error) {
	functions, err := ReadFunctions(cfg.Dir)
	if err != nil {
		return Output{}, err
	}
	prebuilt := Output{Functions: functions}
	for _, app := range ImageApps(cfg.Apps) {
		ref, err := readImageRef(cfg.Dir, app.Name)
		if err != nil {
			return Output{}, err
		}
		if err := t.refuseAbsentImage(ctx, app.Name, ref, archs[app.Name]); err != nil {
			return Output{}, err
		}
		if prebuilt.Images == nil {
			prebuilt.Images = map[string]string{}
		}
		prebuilt.Images[app.Name] = ref
	}
	return prebuilt, nil
}

func (t tools) refuseAbsentImage(ctx context.Context, app, ref, arch string) error {
	repository, digest, _ := strings.Cut(ref, "@")
	holds, err := t.architecture(ctx, repository, digest)
	if err != nil {
		return fmt.Errorf("app %q was prebuilt into the image %s, and the docker daemon cannot hand it over: %w; run `ocel build` again, or deploy without --prebuilt", app, ref, err)
	}
	if arch != "" && holds != arch {
		return fmt.Errorf("app %q was prebuilt into an image for %s, and the target runs %s: declare `arch: %q` on %q and run `ocel build` again, or deploy without --prebuilt", app, images.ContainerPlatform(holds), images.ContainerPlatform(arch), arch, app)
	}
	return nil
}

const imageRefFileName = "image-ref"

func imageRefPath(projectDir, app string) string {
	return filepath.Join(appbuild.AppArtifactRoot(appbuild.ArtifactRoot(projectDir), app), imageRefFileName)
}

func writeImageRef(projectDir, app, ref string) error {
	path := imageRefPath(projectDir, app)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(ref+"\n"), 0o644); err != nil {
		return fmt.Errorf("record the image of %q at %s: %w", app, path, err)
	}
	return nil
}

func readImageRef(projectDir, app string) (string, error) {
	path := imageRefPath(projectDir, app)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("app %q runs in a container, and %s holds no image for it: run `ocel build` with `compute: \"container\"` stated on %q, or deploy without --prebuilt", app, appbuild.ArtifactRootDir, app)
	}
	if err != nil {
		return "", fmt.Errorf("read the image of %q at %s: %w", app, path, err)
	}
	ref := strings.TrimSpace(string(raw))
	if !appbuild.PinnedImage(ref) {
		return "", fmt.Errorf("%s names %q as the image of %q, which pins no digest; run `ocel build` again", path, ref, app)
	}
	return ref, nil
}

func RefuseUnbuildableImages(ctx context.Context, span *run.Span, cfg *projectconfig.Config, archs map[string]string) error {
	var recipes []image.Recipe
	for _, app := range ImageApps(cfg.Apps) {
		described, err := image.Describe(cfg, app)
		if err != nil {
			return err
		}
		recipe, err := image.ChooseRecipe(described)
		if err != nil {
			return err
		}
		recipes = append(recipes, recipe)
	}
	if len(recipes) == 0 {
		return nil
	}
	containers := make([]string, len(recipes))
	for i, recipe := range recipes {
		containers[i] = recipe.App.Name
		if notice := recipe.Notice(); notice != "" {
			span.Say(notice)
		}
	}
	if err := image.RefuseUnusableDaemon(ctx, slices.Sorted(maps.Values(archs))...); err != nil {
		return fmt.Errorf("building the container image for %s happens on this machine, before anything is provisioned:\n    %w", english.And(english.Quoted(containers)), err)
	}
	return nil
}
