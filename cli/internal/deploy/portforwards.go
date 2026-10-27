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

type boundUses struct {
	build, preBuild []portforward.Use
	records         []portforward.Use
}

func deliverForwards(f *portforward.Forwards, values map[string]build.AppVariables) {
	for _, app := range f.Apps() {
		if app == portforward.WholeProject {
			continue
		}
		delivered := appVariablesOf(values, app)
		maps.Copy(delivered.Live, f.Bindings(app))
		delivered.BindingProxyEnv = f.BindingProxyEnv(app)
		values[app] = delivered
	}
}

func deliverRecords(records map[string]map[string]string, values map[string]build.AppVariables) {
	for app, live := range records {
		if app == portforward.WholeProject {
			continue
		}
		delivered := appVariablesOf(values, app)
		maps.Copy(delivered.Live, live)
		values[app] = delivered
	}
}

func appVariablesOf(values map[string]build.AppVariables, app string) build.AppVariables {
	if _, ok := values[app]; !ok {
		values[app] = build.AppVariables{Env: map[string]string{}, Live: map[string]string{}}
	}
	return values[app]
}

func (d *deployRun) forwardPorts(ctx context.Context, steps *buildSteps, bound boundUses) (*portforward.Forwards, error) {
	if !d.forwardsPorts {
		return nil, nil
	}
	cfg := d.cfg
	buildUses, preBuildUses := bound.build, bound.preBuild
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

func (i *infraProvisioning) findBoundUses(cfg *project.Project, resources []declaration.Resource, usages []attribution.Usage, runsPreBuild bool) (boundUses, error) {
	built := map[string]bool{}
	for _, app := range cfg.Apps {
		if app.BuildsWithResources && buildoutput.BuildsWithItsOwnScript(app.Framework) {
			built[app.Name] = true
		}
	}
	if len(built) == 0 && !runsPreBuild {
		return boundUses{}, nil
	}
	infra := i.sent.GetManifest()
	if infra == nil {
		var err error
		if infra, err = i.assemble(resources); err != nil {
			return boundUses{}, err
		}
	}
	var uses boundUses
	for _, usage := range usages {
		_, bindable := bindings.BindableAs(usage.Type)
		if !built[usage.App] || !bindable {
			continue
		}
		if bound, provisioned := portforward.FindBound(infra, usage.Type, usage.Name); provisioned {
			uses.build = append(uses.build, portforward.Use{App: usage.App, Resource: usage.Type, Declared: usage.Name, Bound: bound})
		}
		if record, recorded := portforward.FindRecord(infra, usage.Type, usage.Name); recorded {
			uses.records = append(uses.records, portforward.Use{App: usage.App, Resource: usage.Type, Declared: usage.Name, Bound: record})
		}
	}
	if runsPreBuild {
		uses.preBuild = portforward.ListWholeProjectUses(infra)
		uses.records = append(uses.records, portforward.ListWholeProjectRecords(infra)...)
	}
	return uses, nil
}
