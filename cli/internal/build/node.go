package build

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
	"sync"
	"unicode"

	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/constants"
)

const buildPlanFileName = "build-plan.json"

const traceStrategy = "trace"

const bundleStrategy = "bundle"

type buildPlan struct {
	Functions []functionSummary `json:"functions"`
}

type functionSummary struct {
	Name         string             `json:"name"`
	Framework    appbuild.Framework `json:"framework"`
	Handler      string             `json:"handler"`
	ArtifactPath string             `json:"artifactPath"`
	Strategy     string             `json:"strategy"`
	Entrypoint   string             `json:"entrypoint,omitempty"`
}

type builderRequest struct {
	OutDir        string     `json:"outDir"`
	ProjectRoot   string     `json:"projectRoot"`
	EdgeKind      string     `json:"edgeKind"`
	AllowDegraded []string   `json:"allowDegraded,omitempty"`
	Apps          []appInput `json:"apps"`
}

type appInput struct {
	Name       string            `json:"name"`
	Cwd        string            `json:"cwd"`
	Entrypoint string            `json:"entrypoint,omitempty"`
	Framework  *frameworkInput   `json:"framework,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Folder     string            `json:"folder,omitempty"`
}

func frameworkInputOf(framework projectconfig.Framework) *frameworkInput {
	if framework.Name == "" && framework.Arch == "" {
		return nil
	}
	return &frameworkInput{Name: framework.Name, Arch: framework.Arch}
}

type frameworkInput struct {
	Name string `json:"name,omitempty"`
	Arch string `json:"arch,omitempty"`
}

const adapterPathEnv = "NEXT_ADAPTER_PATH"

var buildOwnedNames = []string{adapterPathEnv, constants.AppFolderEnvName, deploymentIDEnv, constants.PhaseEnvName, "PATH"}

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
	return append(env, adapterPathEnv+"="+adapterPath, constants.AppFolderEnvName+"=")
}

func recordDetectedDeploymentID(projectDir, outputDir, id string) error {
	if id == "" {
		return nil
	}
	entries, err := os.ReadDir(appbuild.AppsRoot(outputDir))
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

func bundlePlanned(ctx context.Context, outputDir string, stderr io.Writer) error {
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
		if err := toolchain.Bundle(ctx, toolchain.Target{
			App:        filepath.Base(appDir),
			Framework:  fn.Framework,
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
	if filepath.Base(functionsDir) != functionsDirName || filepath.Dir(appDir) != appbuild.AppsRoot(outputDir) {
		return "", fmt.Errorf("%s does not sit under %s", funcDir, filepath.Join(appbuild.AppsRoot(outputDir), "<app>", functionsDirName))
	}
	return appDir, nil
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

type appRouting struct {
	log Log

	mu      sync.Mutex
	current *appLog
}

type appLog struct {
	w io.Writer
}

func (r *appRouting) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return r.log.shared().Write(p)
	}
	return r.current.w.Write(p)
}

func (r *appRouting) begin(app string) func(error) {
	w, ended := r.log.App(app)
	log := &appLog{w: w}
	r.mu.Lock()
	r.current = log
	r.mu.Unlock()
	return func(err error) {
		r.mu.Lock()
		if r.current == log {
			r.current = nil
		}
		r.mu.Unlock()
		ended(err)
	}
}

func runNode(ctx context.Context, scriptPath string, env []string, request []byte, log Log) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not found on PATH: %w", err)
	}

	routing := &appRouting{log: log}
	var captured bytes.Buffer

	cmd := exec.CommandContext(ctx, "node", scriptPath)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(request)

	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("node-builder failed: %w", err)
	}
	cmd.Stdout, cmd.Stderr = writer, writer

	proc := &nodeprotocol.Processor{Run: runtrace.FromContext(ctx), Forward: io.MultiWriter(routing, &captured), AppBuild: routing.begin}

	startErr := cmd.Start()
	_ = writer.Close()
	if startErr != nil {
		_ = reader.Close()
		return fmt.Errorf("node-builder failed: %w", startErr)
	}
	proc.Scan(ctx, reader)
	_ = reader.Close()
	runErr := cmd.Wait()
	unended := proc.Abort(ctx)

	if runErr != nil {
		if msg := proc.Failure(); msg != "" {
			return fmt.Errorf("node-builder failed (%w): %s", runErr, msg)
		}
		if summary := failureSummary(captured.String()); summary != "" {
			return fmt.Errorf("node-builder failed (%w): %s", runErr, summary)
		}
		return fmt.Errorf("node-builder failed: %w", runErr)
	}
	if unended != nil {
		return fmt.Errorf("node-builder failed: %w", unended)
	}
	return nil
}
