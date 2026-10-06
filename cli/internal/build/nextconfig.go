package build

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	warning string
}{
	{
		StandaloneOutput,
		regexp.MustCompile("output\\s*:\\s*[\"'`]standalone[\"'`]"),
		`app %q sets output: "standalone" in %[2]s, and a standalone server.js runs on the config its build baked in, so it never loads the cache handlers ocel adds to its image and each instance caches on its own: delete output: "standalone" from %[2]s and start the image with next start`,
	},
	{
		OwnAdapter,
		regexp.MustCompile(`\badapterPath\s*[:,}]`),
		`app %q sets adapterPath in %[2]s, and next start loads that adapter in place of the one ocel adds to its image, so it never loads the cache handlers ocel ships and each instance caches on its own: delete adapterPath from %[2]s`,
	},
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
	code := stripJSComments(text)
	for _, s := range nextConfigSettings {
		if s.pattern.MatchString(code) {
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

func stripJSComments(src string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
			out.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 3
			out.WriteByte(' ')
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

func (c NextConfigConflict) Warning() string {
	for _, s := range nextConfigSettings {
		if s.setting == c.Setting {
			return fmt.Sprintf(s.warning, c.App, c.ConfigFile)
		}
	}
	return ""
}
