package deploy

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/portforward"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/bindings"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func deliverForwards(f *portforward.Forwards, values map[string]build.AppVariables) {
	for _, app := range f.Apps() {
		if app == portforward.WholeProject {
			continue
		}
		if _, ok := values[app]; !ok {
			values[app] = build.AppVariables{Env: map[string]string{}, Live: map[string]string{}}
		}
		delivered := values[app]
		maps.Copy(delivered.Live, f.Bindings(app))
		delivered.BindingProxyEnv = f.BindingProxyEnv(app)
		values[app] = delivered
	}
}

func (d *deployRun) forwardPorts(ctx context.Context, steps *buildSteps, c collected, runsPreBuild bool) (*portforward.Forwards, error) {
	if !d.forwardsPorts {
		return nil, nil
	}
	cfg := d.cfg
	buildUses, preBuildUses, err := d.infra.findBoundUses(cfg, c.resources, c.usages, runsPreBuild)
	if err != nil {
		return nil, err
	}
	uses := slices.Concat(buildUses, preBuildUses)
	if len(uses) == 0 {
		return nil, nil
	}
	declared := portforward.ListDeclared(uses)

	var notReady error
	forwarding := steps.open(cfg.Slug, progress.Forwarding.Title("ports to "+english.And(declared)))
	forwards, err := portforward.Open(ctx, d.provider, cfg.Slug, d.infra.env, uses, steps.phase)
	switch code, refused := provider.RefusedCode(err); {
	case err != nil && len(preBuildUses) > 0:
		err = portforward.RefuseUnforwarded(preBuildName, declared, err)
	case refused && code == refusal.CodeNotReady:
		notReady, err = err, nil
	}
	if notReady != nil {
		forwarding.EndPartial(portforward.DescribeRefused("The build", declared, notReady))
		return nil, nil
	}
	forwarding.End(err)
	if err != nil {
		return nil, err
	}
	unforwarded := forwards.Unforwarded()
	if len(preBuildUses) > 0 && len(unforwarded) > 0 {
		steps.phase.Say(portforward.DescribeUnforwarded(preBuildName, unforwarded))
	}
	buildDeclared := portforward.ListDeclared(buildUses)
	var buildUnforwarded []string
	for _, name := range unforwarded {
		if slices.Contains(buildDeclared, name) {
			buildUnforwarded = append(buildUnforwarded, name)
		}
	}
	if len(buildUnforwarded) > 0 {
		steps.phase.Say(portforward.DescribeUnforwarded("The build", buildUnforwarded))
	}
	return forwards, nil
}

func (i *infraProvisioning) findBoundUses(cfg *project.Project, resources []declaration.Resource, usages []attribution.Usage, runsPreBuild bool) (buildUses, preBuildUses []portforward.Use, err error) {
	built := map[string]bool{}
	for _, app := range cfg.Apps {
		if app.BuildsWithResources && buildoutput.BuildsWithItsOwnScript(app.Framework) {
			built[app.Name] = true
		}
	}
	if len(built) == 0 && !runsPreBuild {
		return nil, nil, nil
	}
	infra := i.sent.GetManifest()
	if infra == nil {
		if infra, err = i.assemble(resources); err != nil {
			return nil, nil, err
		}
	}
	for _, usage := range usages {
		_, bindable := bindings.BindableAs(usage.Type)
		if !built[usage.App] || !bindable {
			continue
		}
		bound, provisioned := portforward.FindBound(infra, usage.Type, usage.Name)
		if !provisioned {
			continue
		}
		buildUses = append(buildUses, portforward.Use{App: usage.App, Resource: usage.Type, Declared: usage.Name, Bound: bound})
	}
	if runsPreBuild {
		preBuildUses = portforward.ListWholeProjectUses(infra)
	}
	return buildUses, preBuildUses, nil
}
