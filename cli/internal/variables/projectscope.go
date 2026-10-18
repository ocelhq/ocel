package variables

import (
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/envsource"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func ScopeOf(cfg *project.Project, tier environmentv1.Tier, environment string) Scope {
	return Scope{
		Apps:        AppsOf(cfg),
		Tier:        tier,
		Environment: environment,
		Bindings:    inlineBindingVariables(cfg, tier),
		OtherTiers:  inlineBindingVariables(cfg, OtherTier(tier)),
		EnvSource:   ConfiguredEnvSource(cfg, tier),
	}
}

func DevScope(cfg *project.Project) Scope {
	return Scope{Apps: AppsOf(cfg)}
}

func OtherTier(tier environmentv1.Tier) environmentv1.Tier {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return environmentv1.Tier_TIER_PRODUCTION
	}
	return environmentv1.Tier_TIER_PREVIEW
}

func inlineBindingVariables(cfg *project.Project, tier environmentv1.Tier) []BindingVariables {
	var out []BindingVariables
	for _, binding := range cfg.BindingsFor(tier) {
		if binding.Inline == nil {
			continue
		}
		out = append(out, BindingVariables{
			Group: binding.Group(),
			Site:  "bindings." + binding.Group(),
			Keys:  binding.Inline.Variables(),
		})
	}
	return out
}

func AppsOf(cfg *project.Project) []App {
	apps := make([]App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		apps = append(apps, App{
			Name:      a.Name,
			Folder:    a.Folder,
			Framework: a.Framework(),
		})
	}
	return apps
}

func EnvSourceDescriptor(cfg *project.Project, tier environmentv1.Tier) envsource.Descriptor {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return cfg.EnvSource.Preview
	}
	return cfg.EnvSource.Production
}

func ConfiguredEnvSource(cfg *project.Project, tier environmentv1.Tier) EnvSource {
	descriptor := EnvSourceDescriptor(cfg, tier)
	return EnvSource{ID: descriptor.ID(), Credentials: descriptor.CredentialVariables()}
}
