package gcp

import (
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/bindings"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/live"
)

func (p *Provider) runtimeEnv(names names.Names, spec provider.StackSpec, tasks *variables.Tasks) (map[string]string, error) {
	app := spec.App
	env := map[string]string{}
	if app.HealthCheckPath != "" {
		env[originguard.HealthPathVar] = app.HealthCheckPath
	}
	manifest, err := variables.Render(variables.Manifest{
		Project:            names.Project(),
		Region:             p.options.Region,
		Namespace:          string(names.Namespace()),
		Slug:               spec.Ref.Project,
		Tier:               string(spec.Ref.Tier),
		Environment:        liveEnvironment(spec.Ref),
		Endpoint:           p.containerEndpoint(),
		Keys:               liveKeys(app.Values),
		Bindings:           liveBindings(app.Values),
		Tasks:              tasks,
		RealtimePublishURL: findRealtimePublishURL(app.Values.Bindings),
	})
	if err != nil {
		return nil, fmt.Errorf("pin %s's live values: %w", app.App, err)
	}
	if len(manifest) > 0 {
		env[variables.EnvVar] = string(manifest)
	}
	return env, nil
}

func liveEnvironment(ref provider.StackRef) string {
	if ref.Tier == environment.TierProduction {
		return ""
	}
	return ref.Name.Env
}

func liveKeys(values provider.AppValues) []live.Key {
	keys := make([]live.Key, 0, len(values.Secrets))
	for _, secret := range values.Secrets {
		keys = append(keys, live.Key{Key: secret.Key, Folder: secret.Folder})
	}
	return keys
}

func liveBindings(values provider.AppValues) []live.Binding {
	declared := make([]live.Binding, 0, len(values.Bindings))
	for _, binding := range values.Bindings {
		kind := provider.ProtoBindingType(binding.Type)
		resource := binding.Resource
		if resource == "" {
			resource = binding.Name
		}
		declared = append(declared, live.Binding{
			Name:    binding.Name,
			Key:     bindings.ResourceEnvName(kind, resource),
			Type:    kind,
			Granted: binding.Version,
		})
	}
	return declared
}

const (
	specValues           = "the environment its own spec names"
	runtimeValues        = "the runtime environment Ocel gives it"
	cachingEdgeValues    = "the caching edge in front of it"
	ocelRouteTableValues = "its route table"
	nextValues           = "the Next runtime"
	cdnPurgeValues       = "Cloud CDN purging"
	nodePurgeValues      = "the revalidation of a node service"
	gateValues           = "the gate it mounts"
)

func mergeValues(what, source string, set, added map[string]string) (map[string]string, error) {
	values := make(map[string]string, len(set)+len(added))
	maps.Copy(values, set)
	for _, name := range slices.Sorted(maps.Keys(added)) {
		if _, taken := values[name]; taken {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"%s takes %s from %s, and the deploy already set a value for %s: "+
					"a revision has one entry per name, so one value would silently take the place of the other "+
					"and the app would read a value nothing in it declared. Rename one of them",
				what, name, source, name)
		}
		values[name] = added[name]
	}
	return values, nil
}
