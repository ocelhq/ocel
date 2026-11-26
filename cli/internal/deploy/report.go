package deploy

import (
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (d *deployRun) appsDeployed(manifest *contractv1.Manifest, results []*progressv1.AppResult) ([]*consolev1.App, error) {
	apps, err := deployreport.AppsDeployed(manifest, results, d.env.GetTier(), func(app string) (string, error) {
		return build.FrameworkBuildID(d.cfg.Dir, app)
	})
	declaredBy := map[string][]*resourcesv1.VariableDefinition{}
	if d.declarations != nil {
		definitions := d.declarations.Definitions()
		for _, app := range variables.AppsOf(d.cfg) {
			declaredBy[app.Name] = variables.DeclaredFor(app, definitions)
		}
	}
	for _, app := range apps {
		app.Variables = declaredBy[app.GetName()]
	}
	return apps, err
}

func (d *deployRun) variableGroups() []*resourcesv1.GroupDefinition {
	if d.declarations == nil {
		return nil
	}
	return d.declarations.Groups()
}

func (o deployOutcome) promotion(tag string) *consolev1.Promotion {
	return &consolev1.Promotion{Id: o.promotionID, Seq: o.promotedAt, Tag: tag}
}
