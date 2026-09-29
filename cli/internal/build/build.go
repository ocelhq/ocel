package build

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/images"
)

type Log struct {
	Shared io.Writer
	Unit   func(app string) (log io.Writer, ended func(error))
}

func (l Log) shared() io.Writer {
	if l.Shared == nil {
		return io.Discard
	}
	return l.Shared
}

func (l Log) App(name string) (io.Writer, func(error)) {
	if l.Unit == nil {
		return l.shared(), func(error) {}
	}
	return l.Unit(name)
}

type Output struct {
	Functions []manifestbuilder.Function
	Images    map[string]string
}

type nodeRun func(ctx context.Context, scriptPath string, env []string, request []byte, log Log) error

type imageBuild func(ctx context.Context, app image.App, arch string, progress io.Writer) (image.Image, error)

type builtArchitecture func(ctx context.Context, repository, digest string) (string, error)

type tools struct {
	node         nodeRun
	image        imageBuild
	architecture builtArchitecture
}

var installed = tools{node: runNode, image: image.Build, architecture: images.BuiltArchitecture}

func Apps(ctx context.Context, cfg *projectconfig.Config, env map[string]map[string]string, archs map[string]string, log Log) (Output, error) {
	return installed.apps(ctx, cfg, env, archs, log)
}

func ReadPrebuilt(ctx context.Context, cfg *projectconfig.Config, archs map[string]string) (Output, error) {
	return installed.readPrebuilt(ctx, cfg, archs)
}

func (t tools) apps(ctx context.Context, cfg *projectconfig.Config, env map[string]map[string]string, archs map[string]string, log Log) (Output, error) {
	if err := t.functions(ctx, cfg, env, log); err != nil {
		return Output{}, err
	}
	images, err := t.images(ctx, cfg, archs, log)
	if err != nil {
		return Output{}, err
	}
	functions, err := ReadFunctions(cfg.Dir)
	if err != nil {
		return Output{}, err
	}
	return Output{Functions: functions, Images: images}, nil
}

func (t tools) functions(ctx context.Context, cfg *projectconfig.Config, envByApp map[string]map[string]string, log Log) error {
	for _, env := range envByApp {
		if err := checkVariableNames(env); err != nil {
			return err
		}
	}

	outputDir := appbuild.ArtifactRoot(cfg.Dir)
	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("reset %s: %w", appbuild.ArtifactRootDir, err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", appbuild.ArtifactRootDir, err)
	}

	deploymentIDs := make(map[string]string, len(cfg.Apps))
	for _, a := range cfg.Apps {
		id, err := mintDeploymentID()
		if err != nil {
			return err
		}
		if err := writeDeploymentID(cfg.Dir, a.Name, id); err != nil {
			return err
		}
		deploymentIDs[a.Name] = id
	}

	req := builderRequest{
		OutDir:        outputDir,
		ProjectRoot:   cfg.Dir,
		EdgeKind:      string(cfg.EdgeID()),
		AllowDegraded: cfg.AllowDegraded,
		Apps:          make([]appInput, 0, len(cfg.Apps)),
	}
	for _, a := range FunctionApps(cfg.Apps) {
		if compiledFromSource(a.Framework.Name) {
			appLog, ended := log.App(a.Name)
			err := compile(ctx, cfg, a, outputDir, appLog)
			ended(err)
			if err != nil {
				return err
			}
			continue
		}
		req.Apps = append(req.Apps, appInput{
			Name:       a.Name,
			Cwd:        filepath.Join(cfg.Dir, a.Path),
			Entrypoint: a.Entrypoint,
			Framework:  frameworkInputOf(a.Framework),
			Env:        withDeploymentID(envByApp[a.Name], deploymentIDs[a.Name]),
			Folder:     a.Folder,
		})
	}
	if len(req.Apps) == 0 {
		if len(cfg.Apps) > 0 {
			return nil
		}
		hasJS, err := discovery.HasJS(cfg)
		if err != nil {
			return err
		}
		if !hasJS {
			return nil
		}
	}

	builderPath := node.BuilderPath(cfg.Dir)
	if _, err := os.Stat(builderPath); err != nil {
		return fmt.Errorf("node builder not found at %s: %w", builderPath, err)
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal build request: %w", err)
	}
	rootEnv := envByApp[rootAppEnv]
	detectedID := ""
	if len(cfg.Apps) == 0 {
		id, err := mintDeploymentID()
		if err != nil {
			return err
		}
		detectedID = id
		rootEnv = withDeploymentID(rootEnv, id)
	}
	if err := t.node(ctx, builderPath, builderEnv(node.AdapterPath(cfg.Dir), rootEnv), payload, log); err != nil {
		return err
	}
	if err := bundlePlanned(ctx, outputDir, log.shared()); err != nil {
		return err
	}
	return recordDetectedDeploymentID(cfg.Dir, outputDir, detectedID)
}
