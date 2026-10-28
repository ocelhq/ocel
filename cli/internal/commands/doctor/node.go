package doctor

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/tracing"
)

func nodeReasons(cfg *project.Project, roots []discovery.Root, rootsErr error) []string {
	var reasons []string
	if strings.HasSuffix(cfg.Path, ".ts") {
		reasons = append(reasons, filepath.Base(cfg.Path)+" is TypeScript")
	}
	if cfg.HasJSApp() {
		reasons = append(reasons, "an app is JavaScript")
	}
	if rootsErr != nil || slices.ContainsFunc(roots, func(root discovery.Root) bool { return root.Language == language.JS }) {
		reasons = append(reasons, "this project declares resources in JavaScript")
	}
	if len(cfg.Transforms) > 0 {
		reasons = append(reasons, "this project lists transforms")
	}
	return reasons
}

func nodeCheck(ctx context.Context, cfg *project.Project, roots []discovery.Root, rootsErr error) (check, bool) {
	reasons := nodeReasons(cfg, roots, rootsErr)
	if len(reasons) == 0 {
		return check{}, false
	}
	needed := "node is needed — " + strings.Join(reasons, ", ")

	path, err := exec.LookPath("node")
	if err != nil {
		return check{verdict: verdictFail, text: needed + " — and it is not on PATH", fix: "install Node.js and put it on PATH"}, true
	}
	out, runErr := tracing.CommandContext(ctx, path, "--version").Output()
	found := strings.TrimSpace(string(out))
	if runErr != nil || found == "" {
		return check{verdict: verdictPass, text: needed + " — node on PATH"}, true
	}
	return check{verdict: verdictPass, text: needed + " — node " + found + " on PATH"}, true
}
