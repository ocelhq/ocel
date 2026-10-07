package deploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/english"
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

func (i *infraProvisioning) forwardPorts(ctx context.Context, steps *buildSteps, cfg *project.Project, resources []declaration.Resource, usages []attribution.Usage) (*portForwards, error) {
	if i == nil || !i.providerProcess.Facts().GetForwardsPorts() {
		return nil, nil
	}
	uses, err := i.findBoundUses(cfg, resources, usages)
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
	var notReady error
	err = steps.run(cfg.Slug, progress.Forwarding.Title("ports to "+english.And(declared)), func() (err error) {
		forwards, err = i.openPortForwards(ctx, steps, uses, &contractv1.ForwardPortsRequest{Slug: cfg.Slug, Environment: i.env, Bindings: names})
		if code, refused := provider.RefusedCode(err); refused && code == refusal.CodeNotReady {
			notReady = err
			return nil
		}
		return err
	})
	if notReady != nil {
		steps.phase.Say(fmt.Sprintf("The build goes without the bindings of %s: %s", english.And(declared), refusalMessage(notReady)))
	}
	return forwards, err
}

func refusalMessage(err error) string {
	var rpcErr *connect.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Message()
	}
	return err.Error()
}

func (i *infraProvisioning) findBoundUses(cfg *project.Project, resources []declaration.Resource, usages []attribution.Usage) ([]appBinding, error) {
	built := map[string]bool{}
	for _, app := range build.FunctionApps(cfg.Apps) {
		if app.BuildsWithBindings && app.Framework() == buildoutput.FrameworkNext {
			built[app.Name] = true
		}
	}
	if len(built) == 0 {
		return nil, nil
	}
	infra := i.sent.GetManifest()
	if infra == nil {
		var err error
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
	if unforwarded := declaredNames(uses, resp.GetUnforwarded()); len(unforwarded) > 0 {
		steps.phase.Say(fmt.Sprintf("The build goes without the bindings of %s, since the provider forwards no port to them", english.And(unforwarded)))
	}
	byApp, err := liveBindings(uses, resp.GetBindings())
	if err != nil {
		return nil, errors.Join(err, forwards.Close())
	}
	forwards.bindings = byApp
	return forwards, nil
}

func declaredNames(uses []appBinding, bound []string) []string {
	var declared []string
	for _, use := range uses {
		if slices.Contains(bound, use.bound) && !slices.Contains(declared, use.declared) {
			declared = append(declared, use.declared)
		}
	}
	slices.Sort(declared)
	return declared
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
