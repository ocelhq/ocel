package variables

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/processenv"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type App struct {
	Name      string
	Folder    string
	Framework string
}

type Scope struct {
	Apps        []App
	Tier        environmentv1.Tier
	Environment string
	Browser     bool
	Bindings    []BindingVariables
	OtherTiers  []BindingVariables
	EnvSource   EnvSource
}

func (s Scope) isPreview() bool {
	return s.Tier == environmentv1.Tier_TIER_PREVIEW
}

func (a App) IsInScope(folders []string) bool {
	return len(folders) == 0 || slices.Contains(folders, a.Folder)
}

func (s Scope) IsWrittenByOcel(key string, folders []string) bool {
	return slices.ContainsFunc(s.Apps, func(app App) bool {
		return app.IsInScope(folders) && processenv.IsInjected(app.Framework, key)
	})
}
