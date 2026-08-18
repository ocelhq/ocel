package appbuilder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/cli/internal/appbundler"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/obs"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const scratchDirName = ".ocel"

const outputDirName = "output"

const appsDirName = "apps"

const functionsDirName = "functions"

const funcDirSuffix = ".func"

const configFileName = "config.json"

const buildPlanFileName = "build-plan.json"

const traceStrategy = "trace"

const bundleStrategy = "bundle"

type buildPlan struct {
	Functions []functionSummary `json:"functions"`
}

type functionSummary struct {
	Name         string `json:"name"`
	Runtime      string `json:"runtime"`
	Handler      string `json:"handler"`
	ArtifactPath string `json:"artifactPath"`
	Framework    string `json:"framework"`
	Strategy     string `json:"strategy"`
	Entrypoint   string `json:"entrypoint,omitempty"`
}

type builderRequest struct {
	OutDir      string     `json:"outDir"`
	ProjectRoot string     `json:"projectRoot"`
	Apps        []appInput `json:"apps"`
}

type appInput struct {
	Name       string            `json:"name"`
	Cwd        string            `json:"cwd"`
	Entrypoint string            `json:"entrypoint,omitempty"`
	Framework  string            `json:"framework,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Folder     string            `json:"folder,omitempty"`
}

type functionConfig struct {
	Runtime   string `json:"runtime"`
	Handler   string `json:"handler"`
	Framework string `json:"framework"`
	App       string `json:"app"`
	ID        string `json:"id,omitempty"`
}

const adapterPathEnv = "NEXT_ADAPTER_PATH"

const appFolderEnv = "OCEL_APP_FOLDER"

var buildOwnedNames = []string{adapterPathEnv, appFolderEnv, deploymentIDEnv, "PATH"}

func checkVariableNames(vars map[string]string) error {
	for _, name := range buildOwnedNames {
		if _, taken := vars[name]; taken {
			return fmt.Errorf("a variable is declared as %s, which the build environment owns; rename it where it is declared", name)
		}
	}
	return nil
}

const rootAppEnv = ""

func builderEnv(adapterPath string, vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	env := os.Environ()
	for _, key := range keys {
		env = append(env, key+"="+vars[key])
	}
	return append(env, adapterPathEnv+"="+adapterPath, appFolderEnv+"=")
}

func AppFolder(apps []projectconfig.App) string {
	if len(apps) == 0 {
		return ""
	}
	folder := apps[0].Folder
	for _, app := range apps[1:] {
		if app.Folder != folder {
			return ""
		}
	}
	return folder
}

type Exec func(ctx context.Context, scriptPath string, env []string, request []byte, stderr io.Writer) error

type Builder struct {
	Exec Exec
}

func Build(ctx context.Context, cfg *projectconfig.Config, envByApp map[string]map[string]string, stderr io.Writer) error {
	return Builder{}.Build(ctx, cfg, envByApp, stderr)
}

func (b Builder) Build(ctx context.Context, cfg *projectconfig.Config, envByApp map[string]map[string]string, stderr io.Writer) error {
	for _, env := range envByApp {
		if err := checkVariableNames(env); err != nil {
			return err
		}
	}

	outputDir := filepath.Join(cfg.Dir, scratchDirName, outputDirName)
	relOutput := filepath.Join(scratchDirName, outputDirName)

	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("reset %s: %w", relOutput, err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", relOutput, err)
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

	builderPath := node.BuilderPath(cfg.Dir)
	if _, err := os.Stat(builderPath); err != nil {
		return fmt.Errorf("node builder not found at %s: %w", builderPath, err)
	}

	req := builderRequest{OutDir: outputDir, ProjectRoot: cfg.Dir, Apps: make([]appInput, 0, len(cfg.Apps))}
	for _, a := range cfg.Apps {
		req.Apps = append(req.Apps, appInput{
			Name:       a.Name,
			Cwd:        filepath.Join(cfg.Dir, a.Path),
			Entrypoint: a.Entrypoint,
			Framework:  a.Framework,
			Env:        withDeploymentID(envByApp[a.Name], deploymentIDs[a.Name]),
			Folder:     a.Folder,
		})
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal build request: %w", err)
	}
	run := b.Exec
	if run == nil {
		run = runNode
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
	if err := run(ctx, builderPath, builderEnv(node.AdapterPath(cfg.Dir), rootEnv), payload, stderr); err != nil {
		return err
	}
	if err := bundlePlanned(outputDir, stderr); err != nil {
		return err
	}
	return recordDetectedDeploymentID(cfg.Dir, outputDir, detectedID)
}

func recordDetectedDeploymentID(projectDir, outputDir, id string) error {
	if id == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(outputDir, appsDirName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := writeDeploymentID(projectDir, entry.Name(), id); err != nil {
			return err
		}
	}
	return nil
}

func bundlePlanned(outputDir string, stderr io.Writer) error {
	planPath := filepath.Join(outputDir, buildPlanFileName)
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("the node builder reported no build plan at %s: %w", planPath, err)
	}
	var plan buildPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return fmt.Errorf("%s: invalid build plan: %w", planPath, err)
	}

	for _, fn := range plan.Functions {
		switch fn.Strategy {
		case traceStrategy:
			continue
		case bundleStrategy:
		default:
			return fmt.Errorf("%s: %q reports build strategy %q, which this build does not know", planPath, fn.Name, fn.Strategy)
		}
		if fn.Entrypoint == "" {
			return fmt.Errorf("%s: %q asks to be bundled without stating an entrypoint", planPath, fn.Name)
		}

		funcDir := filepath.Join(outputDir, filepath.FromSlash(fn.ArtifactPath))
		appDir, err := appArtifactRoot(outputDir, funcDir)
		if err != nil {
			return err
		}
		if err := appbundler.Bundle(appbundler.Target{
			App:        filepath.Base(appDir),
			Framework:  fn.Framework,
			Runtime:    fn.Runtime,
			Entrypoint: fn.Entrypoint,
			FuncDir:    funcDir,
			AppDir:     appDir,
			Log:        stderr,
		}); err != nil {
			return err
		}
	}
	return nil
}

func appArtifactRoot(outputDir, funcDir string) (string, error) {
	functionsDir := filepath.Dir(funcDir)
	appDir := filepath.Dir(functionsDir)
	if filepath.Base(functionsDir) != functionsDirName || filepath.Dir(appDir) != filepath.Join(outputDir, appsDirName) {
		return "", fmt.Errorf("%s does not sit under %s", funcDir, filepath.Join(outputDir, appsDirName, "<app>", functionsDirName))
	}
	return appDir, nil
}

func CollectFunctions(projectDir string) ([]manifestbuilder.Function, error) {
	outputDir := filepath.Join(projectDir, scratchDirName, outputDirName)
	if _, err := os.Stat(outputDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no build output at %s; run `ocel build` first", filepath.Join(scratchDirName, outputDirName))
		}
		return nil, err
	}
	return collectFunctions(outputDir)
}

func EdgeApps(projectDir string) []string {
	appsDir := filepath.Join(projectDir, scratchDirName, outputDirName, appsDirName)
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		return nil
	}

	var apps []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		needs := serveDescriptor(projectDir, entry.Name()).Needs
		if slices.ContainsFunc(edge.CodeNeeds(), func(need edge.Need) bool {
			_, ok := needs[need]
			return ok
		}) {
			apps = append(apps, entry.Name())
		}
	}
	return apps
}

func BuildID(projectDir, app string) string {
	return serveDescriptor(projectDir, app).BuildID
}

func serveDescriptor(projectDir, app string) edge.ServeDescriptor {
	raw, err := os.ReadFile(filepath.Join(projectDir, scratchDirName, outputDirName, appsDirName, app, edge.ServeDescriptorFile))
	if err != nil {
		return edge.ServeDescriptor{}
	}
	var descriptor edge.ServeDescriptor
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		return edge.ServeDescriptor{}
	}
	return descriptor
}

func collectFunctions(outputDir string) ([]manifestbuilder.Function, error) {
	appsDir := filepath.Join(outputDir, appsDirName)
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var functions []manifestbuilder.Function
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		appFunctions, err := collectAppFunctions(outputDir, filepath.Join(appsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		functions = append(functions, appFunctions...)
	}

	slices.SortFunc(functions, func(a, b manifestbuilder.Function) int {
		if byApp := strings.Compare(a.App, b.App); byApp != 0 {
			return byApp
		}
		return strings.Compare(a.Name, b.Name)
	})
	return functions, nil
}

func collectAppFunctions(outputDir, appDir string) ([]manifestbuilder.Function, error) {
	functionsDir := filepath.Join(appDir, functionsDirName)
	if _, err := os.Stat(functionsDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var functions []manifestbuilder.Function
	walkErr := filepath.WalkDir(functionsDir, func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || dir == functionsDir || !strings.HasSuffix(d.Name(), funcDirSuffix) {
			return nil
		}

		fn, err := readFunction(outputDir, functionsDir, dir)
		if err != nil {
			return err
		}
		functions = append(functions, fn)
		return filepath.SkipDir
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return functions, nil
}

func readFunction(outputDir, functionsDir, funcDir string) (manifestbuilder.Function, error) {
	routeRel, err := filepath.Rel(functionsDir, funcDir)
	if err != nil {
		return manifestbuilder.Function{}, err
	}
	artifactRel, err := filepath.Rel(outputDir, funcDir)
	if err != nil {
		return manifestbuilder.Function{}, err
	}
	route := strings.TrimSuffix(filepath.ToSlash(routeRel), funcDirSuffix)

	configPath := filepath.Join(funcDir, configFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifestbuilder.Function{}, fmt.Errorf("%s: missing %s", funcDir, configFileName)
		}
		return manifestbuilder.Function{}, err
	}

	var fc functionConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return manifestbuilder.Function{}, fmt.Errorf("%s: invalid %s: %w", configPath, configFileName, err)
	}
	if fc.Runtime == "" || fc.Handler == "" || fc.Framework == "" || fc.App == "" {
		return manifestbuilder.Function{}, fmt.Errorf("%s: %s requires runtime, handler, framework, and app", configPath, configFileName)
	}

	return manifestbuilder.Function{
		Name:         route,
		Runtime:      fc.Runtime,
		Handler:      fc.Handler,
		ArtifactPath: filepath.ToSlash(artifactRel),
		Framework:    fc.Framework,
		RouteID:      fc.ID,
		App:          fc.App,
	}, nil
}

const summaryLines = 2

func failureSummary(output string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.ContainsFunc(line, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			lines = append(lines, line)
		}
	}
	if len(lines) > summaryLines {
		lines = lines[len(lines)-summaryLines:]
	}
	return strings.Join(lines, "\n")
}

func runNode(ctx context.Context, scriptPath string, env []string, request []byte, stderr io.Writer) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not found on PATH: %w", err)
	}

	var captured bytes.Buffer
	out := io.Writer(&captured)
	if stderr != nil {
		out = io.MultiWriter(stderr, &captured)
	}
	safeErr, safeOut := nodeprotocol.SyncPair(out, out)

	cmd := exec.CommandContext(ctx, "node", scriptPath)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(request)
	cmd.Stderr = safeErr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("node-builder failed: %w", err)
	}

	proc := &nodeprotocol.Processor{Run: obs.FromContext(ctx), Forward: safeOut}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("node-builder failed: %w", err)
	}
	proc.Scan(ctx, stdout)
	runErr := cmd.Wait()

	if runErr != nil {
		proc.Abort()
		if msg := proc.Err(); msg != "" {
			return fmt.Errorf("node-builder failed (%w): %s", runErr, msg)
		}
		if summary := failureSummary(captured.String()); summary != "" {
			return fmt.Errorf("node-builder failed (%w): %s", runErr, summary)
		}
		return fmt.Errorf("node-builder failed: %w", runErr)
	}
	return nil
}
