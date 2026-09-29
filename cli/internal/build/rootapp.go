package build

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/appbuild"
)

const rootAppEnv = ""

const rootAppFallbackName = "app"

var unsafeNameRun = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func appsToBuild(cfg *project.Project) ([]project.App, error) {
	if len(cfg.Apps) > 0 {
		return cfg.Apps, nil
	}
	info, err := os.Stat(filepath.Join(cfg.Dir, "package.json"))
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil
	}
	framework := appbuild.FrameworkNode
	next, err := project.IsNextApp(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if next {
		framework = appbuild.FrameworkNext
	}
	return []project.App{{
		Name:      rootAppName(cfg.Dir),
		Path:      ".",
		Framework: project.Framework{Name: framework, Detected: true},
	}}, nil
}

func rootAppName(dir string) string {
	name := strings.Trim(unsafeNameRun.ReplaceAllString(filepath.Base(dir), "-"), "-")
	if name == "" {
		return rootAppFallbackName
	}
	return name
}
