package build

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/pkg/tracing"
)

type nodeBuildRequest struct {
	Apps []nodeAppBuild `json:"apps"`
}

type nodeAppBuild struct {
	Framework     string            `json:"framework"`
	Name          string            `json:"name"`
	Cwd           string            `json:"cwd"`
	OutputDir     string            `json:"outputDir,omitempty"`
	BuildID       string            `json:"buildId,omitempty"`
	Folder        string            `json:"folder,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Unset         []string          `json:"unset,omitempty"`
	AllowDegraded []string          `json:"allowDegraded,omitempty"`
	PublicKeys    []string          `json:"publicKeys,omitempty"`
	Entrypoint    string            `json:"entrypoint,omitempty"`
	FunctionDir   string            `json:"functionDir,omitempty"`

	EdgeRunsCode       bool                `json:"edgeRunsCode,omitempty"`
	EdgeCacheTagLimits *nodeCacheTagLimits `json:"edgeCacheTagLimits,omitempty"`

	MaxFunctionBytes int64  `json:"maxFunctionBytes,omitempty"`
	Arch             string `json:"arch,omitempty"`
}

type nodeCacheTagLimits struct {
	TagBytes    int64 `json:"tagBytes"`
	Tags        int64 `json:"tags"`
	HeaderBytes int64 `json:"headerBytes"`
}

func newNodeCacheTagLimits(limits CacheTagLimits) *nodeCacheTagLimits {
	if limits == (CacheTagLimits{}) {
		return nil
	}
	return &nodeCacheTagLimits{TagBytes: limits.TagBytes, Tags: limits.Tags, HeaderBytes: limits.HeaderBytes}
}

const summaryLines = 2

const capturedOutputBytes = 64 << 10

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

func runNode(ctx context.Context, scriptPath string, request []byte, log Log) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not found on PATH: %w", err)
	}

	captured := &tracing.Tail{Limit: capturedOutputBytes}

	cmd := tracing.Command(ctx, "node", scriptPath)
	cmd.ExportsSpans = true
	cmd.Stdin = bytes.NewReader(request)

	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("the node build failed: %w", err)
	}
	cmd.Stdout, cmd.Stderr = writer, writer

	appBuild := func(app string) (io.Writer, func(error)) {
		_, w, ended := log.App(ctx, app)
		return io.MultiWriter(w, captured), ended
	}
	child, startErr := childprocess.Start(ctx, cmd, childprocess.Options{})
	_ = writer.Close()
	if startErr != nil {
		_ = reader.Close()
		return fmt.Errorf("the node build failed: %w", startErr)
	}
	proc := &nodeprotocol.Processor{Forward: io.MultiWriter(log.shared(), captured), AppBuild: appBuild, Hide: log.hidden, ChildExportsSpans: cmd.IsExportingSpans()}
	proc.Scan(ctx, reader)
	_ = reader.Close()
	runErr := child.Wait()
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
