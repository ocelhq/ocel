package build

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

var nextConfigFiles = []string{"next.config.js", "next.config.mjs", "next.config.cjs", "next.config.ts", "next.config.mts"}

type NextConfigSetting int

const (
	StandaloneOutput NextConfigSetting = iota + 1
	OwnAdapter
)

type NextConfigConflict struct {
	App        string
	ConfigFile string
	Setting    NextConfigSetting
}

var nextConfigSettings = []struct {
	setting NextConfigSetting
	pattern *regexp.Regexp
}{
	{StandaloneOutput, regexp.MustCompile("output\\s*:\\s*[\"'`]standalone[\"'`]")},
	{OwnAdapter, regexp.MustCompile(`\badapterPath\s*:`)},
}

func FindNextConfigConflicts(cfg *project.Project, host Host) ([]NextConfigConflict, error) {
	if !host.ShipsNextServerRuntime {
		return nil, nil
	}
	var found []NextConfigConflict
	for _, a := range ImageApps(cfg.Apps) {
		if a.Framework() != buildoutput.FrameworkNext {
			continue
		}
		conflicts, err := findAppNextConfigConflicts(cfg, a)
		if err != nil {
			return nil, err
		}
		found = append(found, conflicts...)
	}
	return found, nil
}

func RefuseNextFunctionsWithOwnAdapter(cfg *project.Project) error {
	var refusals []error
	for _, a := range FunctionApps(cfg.Apps) {
		if a.Framework() != buildoutput.FrameworkNext {
			continue
		}
		conflicts, err := findAppNextConfigConflicts(cfg, a)
		if err != nil {
			return err
		}
		for _, c := range conflicts {
			if c.Setting == OwnAdapter {
				refusals = append(refusals, c.functionRefusal())
			}
		}
	}
	return errors.Join(refusals...)
}

func findAppNextConfigConflicts(cfg *project.Project, a project.App) ([]NextConfigConflict, error) {
	file, text, err := readNextConfig(filepath.Join(cfg.Dir, a.Path))
	if err != nil {
		return nil, err
	}
	var found []NextConfigConflict
	for _, s := range nextConfigSettings {
		if s.pattern.MatchString(text) {
			found = append(found, NextConfigConflict{App: a.Name, ConfigFile: file, Setting: s.setting})
		}
	}
	return found, nil
}

func (c NextConfigConflict) functionRefusal() error {
	return fmt.Errorf(`app %q sets adapterPath in %[2]s, and next build then runs that adapter in place of ocel's, which writes the output ocel deploys: delete adapterPath from %[2]s`, c.App, c.ConfigFile)
}

func readNextConfig(dir string) (file, text string, err error) {
	for _, name := range nextConfigFiles {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		return name, string(body), nil
	}
	return "", "", nil
}

func (c NextConfigConflict) Warning() string {
	switch c.Setting {
	case OwnAdapter:
		return fmt.Sprintf(`app %q sets adapterPath in %[2]s, and next start loads that adapter in place of the one ocel adds to its image, so it never loads the cache handlers ocel ships and each instance caches on its own: delete adapterPath from %[2]s`, c.App, c.ConfigFile)
	default:
		return fmt.Sprintf(`app %q sets output: "standalone" in %[2]s, and a standalone server.js runs on the config its build baked in, so it never loads the cache handlers ocel adds to its image and each instance caches on its own: delete output: "standalone" from %[2]s and start the image with next start`, c.App, c.ConfigFile)
	}
}
