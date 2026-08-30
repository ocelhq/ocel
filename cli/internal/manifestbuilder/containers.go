package manifestbuilder

import (
	"fmt"
	"slices"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const DefaultHealthCheckPath = "/"

func buildContainers(manifestApps []*contractv1.ManifestApp, apps []App, functions []Function) ([]*contractv1.ManifestContainer, error) {
	byName := make(map[string]App, len(apps))
	for _, a := range apps {
		byName[a.Name] = a
	}
	packed := make(map[string]bool, len(functions))
	for _, f := range functions {
		packed[f.App] = true
	}

	containers := make([]*contractv1.ManifestContainer, 0, len(manifestApps))
	for _, a := range manifestApps {
		if a.GetCompute() != string(providerkit.ComputeContainer) {
			continue
		}
		name := a.GetName()
		configured, named := byName[name]
		if !named {
			return nil, fmt.Errorf("manifestbuilder: app %q runs on container compute and this project's config does not name it, so there is no directory to build its image from: give %q a name and a path under `apps`", name, name)
		}
		if configured.Image == "" {
			return nil, fmt.Errorf("manifestbuilder: app %q runs on container compute and carries no image, so the manifest would hand a provider an app with nothing to run", name)
		}
		if !providerkit.PinnedImage(configured.Image) {
			return nil, fmt.Errorf("manifestbuilder: app %q carries image %q, and a release pins one repository at one digest: a tag repoints under a running release, so it never rides in the identity", name, configured.Image)
		}
		if packed[name] {
			return nil, fmt.Errorf("manifestbuilder: app %q runs on container compute and was packed into functions as well, so two things would answer the same request", name)
		}
		path := configured.HealthCheckPath
		if path == "" {
			path = DefaultHealthCheckPath
		}
		containers = append(containers, &contractv1.ManifestContainer{
			App:             name,
			Image:           configured.Image,
			HealthCheckPath: path,
		})
	}
	if len(containers) == 0 {
		return nil, nil
	}
	slices.SortFunc(containers, func(a, b *contractv1.ManifestContainer) int {
		return strings.Compare(a.GetApp(), b.GetApp())
	})
	return containers, nil
}
