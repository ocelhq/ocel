package release

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
	manifest "github.com/ocelhq/ocel/platform/gcp/provider/resources/variables/live"
)

func (p *Release) RuntimeEnv(names names.Names, spec provider.StackSpec, tasks *manifest.Tasks, databases map[string]string) (map[string]string, error) {
	app := spec.App
	env := map[string]string{}
	if app.HealthCheckPath != "" {
		env[originguard.HealthPathVar] = app.HealthCheckPath
	}
	rendered, err := manifest.Render(manifest.Manifest{
		Project:            names.Project(),
		Region:             p.Region,
		Namespace:          string(names.Namespace()),
		Slug:               spec.Ref.Project,
		Tier:               string(spec.Ref.Tier),
		Environment:        variables.LiveEnvironment(spec.Ref),
		Endpoint:           p.ContainerEndpoint,
		Databases:          databases,
		Keys:               app.Values.LiveKeys(),
		Bindings:           app.Values.LiveBindings(),
		Tasks:              tasks,
		RealtimePublishURL: realtime.FindPublishURL(app.Values.Bindings),
	})
	if err != nil {
		return nil, fmt.Errorf("pin %s's live values: %w", app.App, err)
	}
	if len(rendered) > 0 {
		env[manifest.EnvVar] = string(rendered)
	}
	return env, nil
}

const (
	SpecValues           = "the environment its own spec names"
	RuntimeValues        = "the runtime environment Ocel gives it"
	CachingEdgeValues    = "the caching edge in front of it"
	OcelRouteTableValues = "its route table"
	NextValues           = "the Next runtime"
	CDNPurgeValues       = "Cloud CDN purging"
	NodePurgeValues      = "the revalidation of a node service"
)
