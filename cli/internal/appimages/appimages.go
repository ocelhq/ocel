package appimages

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/provider"
)

func Build(ctx context.Context, cfg *projectconfig.Config, archs map[string]string, out appbuilder.Output) (map[string]string, error) {
	var refs map[string]string
	for _, app := range Apps(cfg) {
		described, err := image.Describe(cfg, app)
		if err != nil {
			return nil, err
		}
		log, ended := out.App(app.Name)
		built, err := image.Build(ctx, described, archs[app.Name], log)
		ended(err)
		if err != nil {
			return nil, err
		}
		if refs == nil {
			refs = map[string]string{}
		}
		refs[app.Name] = built.Ref
	}
	return refs, nil
}

func Apps(cfg *projectconfig.Config) []projectconfig.App {
	var containers []projectconfig.App
	for _, app := range cfg.Apps {
		if app.Compute == string(provider.ComputeContainer) {
			containers = append(containers, app)
		}
	}
	return containers
}
