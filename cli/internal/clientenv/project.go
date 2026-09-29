package clientenv

import (
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/appbuild"
)

func GenerateProjectAccessors(cfg *projectconfig.Config, keys []Key) (int, error) {
	apps := []App{{Dir: cfg.Dir, ClientBundle: language.HasClientBundle(appbuild.FrameworkNode, cfg.Dir)}}
	if len(cfg.Apps) > 0 {
		apps = apps[:0]
		for _, a := range cfg.Apps {
			dir := filepath.Join(cfg.Dir, a.Path)
			apps = append(apps, App{Name: a.Name, Dir: dir, ClientBundle: language.HasClientBundle(a.Framework.Name, dir)})
		}
	}
	named := 0
	for _, app := range apps {
		if err := GenerateKeys(cfg.Dir, app, keys); err != nil {
			return 0, err
		}
		if err := MapAppEnvImport(cfg.Dir, app); err != nil {
			return 0, err
		}
		named = max(named, len(Offered(keys, app.ClientBundle)))
	}
	return named, nil
}
