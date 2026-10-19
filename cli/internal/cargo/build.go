package cargo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
)

type Build struct {
	Crate Package
	Bin   string
	Dir   string
}

func (b Build) Run(ctx context.Context) (executable string, err error) {
	cmd := exec.Command("cargo", "build", "--quiet", "--message-format=json-render-diagnostics", "--manifest-path", b.Crate.ManifestPath, "--bin", b.Bin)
	cmd.Dir = b.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := childprocess.Run(ctx, cmd); err != nil {
		if said := strings.TrimSpace(stderr.String()); said != "" {
			return "", fmt.Errorf("cargo build of the binary %q: %s", b.Bin, said)
		}
		return "", fmt.Errorf("cargo build of the binary %q: %w", b.Bin, err)
	}
	return b.findExecutable(&stdout)
}

type artifact struct {
	Reason     string `json:"reason"`
	PackageID  string `json:"package_id"`
	Target     Target `json:"target"`
	Executable string `json:"executable"`
}

func (b Build) findExecutable(messages io.Reader) (string, error) {
	lines := bufio.NewScanner(messages)
	lines.Buffer(nil, 64<<20)
	for lines.Scan() {
		var built artifact
		if json.Unmarshal(lines.Bytes(), &built) != nil {
			continue
		}
		if built.Reason == "compiler-artifact" && built.PackageID == b.Crate.ID && built.Target.Name == b.Bin && slices.Contains(built.Target.Kind, "bin") && built.Executable != "" {
			return built.Executable, nil
		}
	}
	if err := lines.Err(); err != nil {
		return "", fmt.Errorf("cargo build of the binary %q: read what cargo said: %w", b.Bin, err)
	}
	return "", fmt.Errorf("cargo build of the binary %q named no executable of the crate %s", b.Bin, b.Crate.Name)
}
