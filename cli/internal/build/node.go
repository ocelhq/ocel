package build

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unicode"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/processenv"
)

type nodeBuildRequest struct {
	Apps []nodeAppBuild `json:"apps"`
}

type nodeAppBuild struct {
	Framework     string            `json:"framework"`
	Name          string            `json:"name"`
	Cwd           string            `json:"cwd"`
	OutputDir     string            `json:"outputDir,omitempty"`
	DeploymentID  string            `json:"deploymentId,omitempty"`
	Folder        string            `json:"folder,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	EdgeKind      string            `json:"edgeKind,omitempty"`
	AllowDegraded []string          `json:"allowDegraded,omitempty"`
	Entrypoint    string            `json:"entrypoint,omitempty"`
	FuncDir       string            `json:"funcDir,omitempty"`
}

var buildOwnedNames = []string{processenv.AppFolderEnvVar, processenv.PhaseEnvVar, "PATH"}

func checkVariableNames(variables map[string]string) error {
	for _, name := range buildOwnedNames {
		if _, taken := variables[name]; taken {
			return fmt.Errorf("a variable is declared as %s, which the build environment owns; rename it where it is declared", name)
		}
	}
	return nil
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

func runNode(ctx context.Context, scriptPath string, request []byte, log Log) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not found on PATH: %w", err)
	}

	routing := &appRouting{log: log}
	var captured bytes.Buffer

	cmd := exec.CommandContext(ctx, "node", scriptPath)
	cmd.Stdin = bytes.NewReader(request)

	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("the node build failed: %w", err)
	}
	cmd.Stdout, cmd.Stderr = writer, writer

	proc := &nodeprotocol.Processor{Span: run.SpanFromContext(ctx), Forward: io.MultiWriter(routing, &captured), AppBuild: routing.begin}

	startErr := cmd.Start()
	_ = writer.Close()
	if startErr != nil {
		_ = reader.Close()
		return fmt.Errorf("the node build failed: %w", startErr)
	}
	proc.Scan(ctx, reader)
	_ = reader.Close()
	runErr := cmd.Wait()
	unended := proc.Abort(ctx)

	if runErr != nil {
		if msg := proc.Failure(); msg != "" {
			return fmt.Errorf("the node build failed (%w): %s", runErr, msg)
		}
		if summary := failureSummary(captured.String()); summary != "" {
			return fmt.Errorf("the node build failed (%w): %s", runErr, summary)
		}
		return fmt.Errorf("the node build failed: %w", runErr)
	}
	if unended != nil {
		return fmt.Errorf("the node build failed: %w", unended)
	}
	return nil
}
