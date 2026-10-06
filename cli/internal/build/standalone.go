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

var standaloneOutput = regexp.MustCompile("output\\s*:\\s*[\"'`]standalone[\"'`]")

type StandaloneNextApp struct{ App, ConfigFile string }

func FindStandaloneNextApps(cfg *project.Project, host Host) ([]StandaloneNextApp, error) {
	if !host.ShipsNextServerRuntime {
		return nil, nil
	}
	var found []StandaloneNextApp
	for _, a := range ImageApps(cfg.Apps) {
		if a.Framework() != buildoutput.FrameworkNext {
			continue
		}
		file, text, err := readNextConfig(filepath.Join(cfg.Dir, a.Path))
		if err != nil {
			return nil, err
		}
		if standaloneOutput.MatchString(text) {
			found = append(found, StandaloneNextApp{App: a.Name, ConfigFile: file})
		}
	}
	return found, nil
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

func (s StandaloneNextApp) Warning() string {
	return fmt.Sprintf(`app %q sets output: "standalone" in %[2]s, and a standalone server.js runs on the config its build baked in, so it never loads the cache handlers ocel adds to its image and each instance caches on its own: delete output: "standalone" from %[2]s and start the image with next start`, s.App, s.ConfigFile)
}
