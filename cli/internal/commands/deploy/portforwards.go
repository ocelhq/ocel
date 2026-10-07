package deploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type appBinding struct {
	app      string
	resource resourcesv1.ResourceType
	declared string
	bound    string
}

type portForwards struct {
	bindings map[string]map[string]string
	stop     context.CancelFunc
	ended    chan error
}

func (f *portForwards) Close() error {
	if f == nil {
		return nil
	}
	f.stop()
	ended := <-f.ended
	if providerprocess.IsCancelled(ended) {
		ended = nil
	}
	return ended
}

func (f *portForwards) deliver(values map[string]build.AppVariables) {
	if f == nil {
		return
	}
	for app, bindings := range f.bindings {
		if _, ok := values[app]; !ok {
			values[app] = build.AppVariables{Env: map[string]string{}, Live: map[string]string{}}
		}
		maps.Copy(values[app].Live, bindings)
	}
}

func (i *infraProvisioning) forwardPorts(ctx context.Context, steps *buildSteps, cfg *project.Project, resources []declaration.Resource) (*portForwards, error) {
	if i == nil || !i.providerProcess.Facts().GetForwardsPorts() {
		return nil, nil
	}
	uses, err := i.findBoundUses(ctx, cfg, resources)
	if err != nil || len(uses) == 0 {
		return nil, err
	}
	var names, declared []string
	for _, use := range uses {
		if !slices.Contains(names, use.bound) {
			names = append(names, use.bound)
		}
		if !slices.Contains(declared, use.declared) {
			declared = append(declared, use.declared)
		}
	}
	slices.Sort(names)
	slices.Sort(declared)

	var forwards *portForwards
	unpublished := false
	err = steps.run(cfg.Slug, progress.Forwarding.Title("ports to "+english.And(declared)), func() (err error) {
		forwards, err = i.openPortForwards(ctx, steps, uses, &contractv1.ForwardPortsRequest{Slug: cfg.Slug, Environment: i.env, Bindings: names})
		if code, refused := provider.RefusedCode(err); i.dry && refused && code == refusal.CodeNotReady {
			unpublished = true
			return nil
		}
		return err
	})
	if unpublished {
		why := "it is not deployed yet"
		if len(declared) > 1 {
			why = "they are not all deployed yet"
		}
		steps.phase.Say(fmt.Sprintf("The build goes without the bindings of %s, since %s", english.And(declared), why))
	}
	return forwards, err
}

func (i *infraProvisioning) findBoundUses(ctx context.Context, cfg *project.Project, resources []declaration.Resource) ([]appBinding, error) {
	built := map[string]bool{}
	for _, app := range build.FunctionApps(cfg.Apps) {
		if app.BuildsWithBindings && app.Framework() == buildoutput.FrameworkNext {
			built[app.Name] = true
		}
	}
	if len(built) == 0 {
		return nil, nil
	}
	usages, err := manifest.FindUsages(ctx, cfg, build.Output{}, resources)
	if err != nil {
		return nil, err
	}
	infra := i.sent.GetManifest()
	if infra == nil {
		if infra, err = i.assemble(resources); err != nil {
			return nil, err
		}
	}
	var uses []appBinding
	for _, usage := range usages {
		_, bindable := naming.BindableAs(usage.Type)
		if !built[usage.App] || !bindable {
			continue
		}
		bound, provisioned := provisionedBinding(infra, usage.Type, usage.Name)
		if !provisioned {
			continue
		}
		uses = append(uses, appBinding{app: usage.App, resource: usage.Type, declared: usage.Name, bound: bound})
	}
	return uses, nil
}

func provisionedBinding(infra *contractv1.Manifest, kind resourcesv1.ResourceType, name string) (string, bool) {
	for _, resource := range infra.GetResources() {
		declared := resource.GetResource()
		if declared.GetType() == kind && declared.GetName() == name && resource.GetBinding() == "" {
			return resource.GetLogicalName(), true
		}
	}
	return "", false
}

func (i *infraProvisioning) openPortForwards(ctx context.Context, steps *buildSteps, uses []appBinding, req *contractv1.ForwardPortsRequest) (*portForwards, error) {
	streamCtx, stop := context.WithCancel(ctx)
	answered := make(chan *contractv1.ForwardPortsResponse, 1)
	ended := make(chan error, 1)
	go func() {
		ended <- providerprocess.ForwardPorts(streamCtx, i.providerProcess, req, func(resp *contractv1.ForwardPortsResponse) {
			select {
			case answered <- resp:
			default:
			}
		})
	}()
	forwards := &portForwards{stop: stop, ended: ended}
	resp, err := awaitForwardsAnswer(answered, ended)
	if err != nil {
		stop()
		return nil, err
	}
	if unforwarded := resp.GetUnforwarded(); len(unforwarded) > 0 {
		steps.phase.Say(fmt.Sprintf("The build goes without the bindings of %s, since the provider forwards no port to them", english.And(unforwarded)))
	}
	byApp, err := liveBindings(uses, resp.GetBindings())
	if err != nil {
		return nil, errors.Join(err, forwards.Close())
	}
	forwards.bindings = byApp
	return forwards, nil
}

func awaitForwardsAnswer(answered <-chan *contractv1.ForwardPortsResponse, ended chan error) (*contractv1.ForwardPortsResponse, error) {
	select {
	case resp := <-answered:
		return resp, nil
	case err := <-ended:
		select {
		case resp := <-answered:
			ended <- err
			return resp, nil
		default:
		}
		if err == nil {
			err = errors.New("provider: ForwardPorts ended without saying which ports it forwarded")
		}
		return nil, err
	}
}

func liveBindings(uses []appBinding, forwarded []*bindingsv1.Binding) (map[string]map[string]string, error) {
	byApp := map[string]map[string]string{}
	for _, use := range uses {
		at := slices.IndexFunc(forwarded, func(binding *bindingsv1.Binding) bool { return binding.GetName() == use.bound })
		if at < 0 {
			continue
		}
		named := proto.Clone(forwarded[at]).(*bindingsv1.Binding)
		named.Name = use.declared
		encoded, err := binding.Encode(use.resource, named)
		if err != nil {
			return nil, fmt.Errorf("encode the binding of %s for the build: %w", use.declared, err)
		}
		if byApp[use.app] == nil {
			byApp[use.app] = map[string]string{}
		}
		maps.Copy(byApp[use.app], encoded.Env)
	}
	return byApp, nil
}
