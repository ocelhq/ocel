package clientenv

import (
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func GenerateProjectAccessors(cfg *project.Project, keys []Key) (named int, files []string, err error) {
	apps := make([]App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		dir := filepath.Join(cfg.Dir, a.Path)
		apps = append(apps, App{Name: a.Name, Dir: dir, ClientBundle: language.HasClientBundle(a.Framework(), dir)})
	}
	for _, app := range apps {
		if err := GenerateKeys(cfg.Dir, app, keys); err != nil {
			return 0, nil, err
		}
		config, err := MapAppEnvImport(cfg.Dir, app)
		if err != nil {
			return 0, nil, err
		}
		for _, path := range []string{accessorPath(cfg.Dir, app.Name, app.Dir), config} {
			if path != "" && !slices.Contains(files, path) {
				files = append(files, path)
			}
		}
		named = max(named, len(Offered(keys, app.ClientBundle)))
	}
	return named, files, nil
}
