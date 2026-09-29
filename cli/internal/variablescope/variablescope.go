package variablescope

import (
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/envsource"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const RootApp = "this project's app"

func Of(cfg *projectconfig.Config, tier environmentv1.Tier, environment string) variables.Scope {
	return variables.Scope{
		Apps:        Apps(cfg),
		Tier:        tier,
		Environment: environment,
		Bindings:    BindingVariables(cfg, tier),
		OtherTiers:  BindingVariables(cfg, OtherTier(tier)),
		EnvSource:   ConfiguredEnvSource(cfg, tier),
	}
}

func ForDev(cfg *projectconfig.Config) variables.Scope {
	return variables.Scope{Apps: Apps(cfg)}
}

func OtherTier(tier environmentv1.Tier) environmentv1.Tier {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return environmentv1.Tier_TIER_PRODUCTION
	}
	return environmentv1.Tier_TIER_PREVIEW
}

func BindingVariables(cfg *projectconfig.Config, tier environmentv1.Tier) []variables.BindingVariables {
	var out []variables.BindingVariables
	for _, binding := range cfg.BindingsFor(tier) {
		if binding.Inline == nil {
			continue
		}
		out = append(out, variables.BindingVariables{
			Group: binding.Group(),
			Site:  "bindings." + binding.Group(),
			Keys:  binding.Inline.Variables(),
		})
	}
	return out
}

func Apps(cfg *projectconfig.Config) []variables.App {
	if len(cfg.Apps) == 0 {
		return []variables.App{{Name: RootApp, ClientBundle: language.HasClientBundle(appbuild.FrameworkNode, cfg.Dir)}}
	}
	apps := make([]variables.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		apps = append(apps, variables.App{
			Name:         a.Name,
			Folder:       a.Folder,
			ClientBundle: language.HasClientBundle(a.Framework.Name, filepath.Join(cfg.Dir, a.Path)),
		})
	}
	return apps
}

func EnvSourceDescriptor(cfg *projectconfig.Config, tier environmentv1.Tier) envsource.Descriptor {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return cfg.EnvSource.Preview
	}
	return cfg.EnvSource.Production
}

func ConfiguredEnvSource(cfg *projectconfig.Config, tier environmentv1.Tier) variables.EnvSource {
	descriptor := EnvSourceDescriptor(cfg, tier)
	return variables.EnvSource{ID: descriptor.ID(), Credentials: descriptor.CredentialVariables()}
}
