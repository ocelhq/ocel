package doctor

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func nodeReasons(cfg *projectconfig.Config) []string {
	var reasons []string
	if strings.HasSuffix(cfg.Path, ".ts") {
		reasons = append(reasons, filepath.Base(cfg.Path)+" is TypeScript")
	}
	if holds, err := discovery.HoldsJS(cfg); err != nil || holds {
		reasons = append(reasons, "this project holds JavaScript")
	}
	if transformed(cfg.Provider) {
		reasons = append(reasons, "the provider is configured with transforms")
	}
	return reasons
}

func transformed(descriptor *projectconfig.ProviderDescriptor) bool {
	if descriptor == nil || len(descriptor.Options) == 0 {
		return false
	}
	var options struct {
		Transforms []string `json:"transforms"`
	}
	if err := json.Unmarshal(descriptor.Options, &options); err != nil {
		return false
	}
	return len(options.Transforms) > 0
}

func nodeCheck(ctx context.Context, cfg *projectconfig.Config) (check, bool) {
	reasons := nodeReasons(cfg)
	if len(reasons) == 0 {
		return check{}, false
	}
	needed := "node is needed — " + strings.Join(reasons, ", ")

	path, err := exec.LookPath("node")
	if err != nil {
		return check{verdict: verdictFail, text: needed + " — and it is not on PATH", fix: "install Node.js and put it on PATH"}, true
	}
	out, runErr := exec.CommandContext(ctx, path, "--version").Output()
	found := strings.TrimSpace(string(out))
	if runErr != nil || found == "" {
		return check{verdict: verdictPass, text: needed + " — node on PATH"}, true
	}
	return check{verdict: verdictPass, text: needed + " — node " + found + " on PATH"}, true
}
