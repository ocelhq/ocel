package variables

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/processenv"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type App struct {
	Name         string
	Folder       string
	Framework    string
	ClientBundle bool
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

func (s Scope) IsWrittenByOcel(key string, folders []string) bool {
	return slices.ContainsFunc(s.Apps, func(app App) bool {
		reached := len(folders) == 0 || slices.Contains(folders, app.Folder)
		return reached && processenv.IsInjected(app.Framework, app.ClientBundle, key)
	})
}
