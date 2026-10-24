package doctor

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func appsCheck(cfg *project.Project, roots []discovery.Root, rootsErr error) check {
	if rootsErr != nil {
		return check{verdict: verdictFail, text: "config loads — " + firstLine(rootsErr.Error())}
	}
	if len(cfg.Apps) > 0 {
		names := make([]string, 0, len(cfg.Apps))
		for _, app := range cfg.Apps {
			names = append(names, app.Name)
		}
		return check{verdict: verdictPass, text: fmt.Sprintf("config loads — %s (%s)", english.Count(len(names), "app"), strings.Join(names, ", ")), detail: cfg.DescribeFrameworks()}
	}
	config := filepath.Base(cfg.Path)
	undetected := "no app is detected at the project root"
	if cfg.RootAppUndetected != nil {
		undetected = cfg.RootAppUndetected.Error()
	}
	if len(roots) == 0 {
		return check{
			verdict: verdictWarn,
			text:    "config loads — nothing to deploy: no apps or resources declared, and " + undetected,
			fix:     `name each app under "apps" in ` + config + " with its name and path",
		}
	}
	if cfg.HasUnclearRootApp() {
		return check{
			verdict: verdictWarn,
			text:    "config loads — no apps declared, and " + undetected,
			fix:     `name the app to deploy under "apps" in ` + config + " with its name and path",
		}
	}
	folders := make([]string, 0, len(roots))
	for _, root := range roots {
		folders = append(folders, rootName(cfg.Dir, root))
	}
	return check{verdict: verdictPass, text: "config loads — no apps, only the resources in " + english.And(folders)}
}

func rootName(projectDir string, root discovery.Root) string {
	folder, err := filepath.Rel(projectDir, root.Dir)
	switch {
	case err != nil:
		return root.Dir
	case folder != ".":
		return filepath.ToSlash(folder)
	case root.Language == language.Rust:
		return "the root crate"
	}
	return "the project root"
}
