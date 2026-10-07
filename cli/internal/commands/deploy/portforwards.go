package deploy

import (
	"context"
	"fmt"
	"maps"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/portforward"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func deliverForwards(f *portforward.Forwards, values map[string]build.AppVariables) {
	for _, app := range f.Apps() {
		if app == portforward.Project {
			continue
		}
		if _, ok := values[app]; !ok {
			values[app] = build.AppVariables{Env: map[string]string{}, Live: map[string]string{}}
		}
		maps.Copy(values[app].Live, f.Bindings(app))
	}
}

func (i *infraProvisioning) forwardPorts(ctx context.Context, steps *buildSteps, cfg *project.Project, resources []declaration.Resource, usages []attribution.Usage, wholeProject bool) (*portforward.Forwards, error) {
	if i == nil || !i.providerProcess.Facts().GetForwardsPorts() {
		return nil, nil
	}
	uses, err := i.findBoundUses(cfg, resources, usages, wholeProject)
	if err != nil || len(uses) == 0 {
		return nil, err
	}
	names, declared := portforward.Names(uses)

	var forwards *portforward.Forwards
	var notReady error
	err = steps.run(cfg.Slug, progress.Forwarding.Title("ports to "+english.And(declared)), func() (err error) {
		forwards, err = portforward.Open(ctx, i.providerProcess, uses, &contractv1.ForwardPortsRequest{Slug: cfg.Slug, Environment: i.env, Bindings: names})
		if code, refused := provider.RefusedCode(err); refused && code == refusal.CodeNotReady {
			notReady = err
			return nil
		}
		if err == nil && len(forwards.Unforwarded()) > 0 {
			steps.phase.Say(fmt.Sprintf("The build goes without the bindings of %s, since the provider forwards no port to them", english.And(forwards.Unforwarded())))
		}
		return err
	})
	if notReady != nil {
		steps.phase.Say(fmt.Sprintf("The build goes without the bindings of %s: %s", english.And(declared), portforward.RefusalMessage(notReady)))
	}
	return forwards, err
}

func (i *infraProvisioning) findBoundUses(cfg *project.Project, resources []declaration.Resource, usages []attribution.Usage, wholeProject bool) ([]portforward.Use, error) {
	built := map[string]bool{}
	for _, app := range build.FunctionApps(cfg.Apps) {
		if app.BuildsWithBindings && app.Framework() == buildoutput.FrameworkNext {
			built[app.Name] = true
		}
	}
	if len(built) == 0 && !wholeProject {
		return nil, nil
	}
	infra := i.sent.GetManifest()
	if infra == nil {
		var err error
		if infra, err = i.assemble(resources); err != nil {
			return nil, err
		}
	}
	var uses []portforward.Use
	for _, usage := range usages {
		_, bindable := naming.BindableAs(usage.Type)
		if !built[usage.App] || !bindable {
			continue
		}
		bound, provisioned := portforward.Provisioned(infra, usage.Type, usage.Name)
		if !provisioned {
			continue
		}
		uses = append(uses, portforward.Use{App: usage.App, Resource: usage.Type, Declared: usage.Name, Bound: bound})
	}
	if wholeProject {
		uses = append(uses, provisionedUses(infra)...)
	}
	return uses, nil
}

func provisionedUses(infra *contractv1.Manifest) []portforward.Use {
	var uses []portforward.Use
	for _, resource := range infra.GetResources() {
		declared := resource.GetResource()
		if _, bindable := naming.BindableAs(declared.GetType()); !bindable || resource.GetBinding() != "" {
			continue
		}
		uses = append(uses, portforward.Use{App: portforward.Project, Resource: declared.GetType(), Declared: declared.GetName(), Bound: resource.GetLogicalName()})
	}
	return uses
}
