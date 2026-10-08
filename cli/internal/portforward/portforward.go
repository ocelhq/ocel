package portforward

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const WholeProject = ""

type Use struct {
	App      string
	Resource resourcesv1.ResourceType
	Declared string
	Bound    string
}

type Forwards struct {
	bindings        map[string]map[string]string
	bindingProxyEnv map[string]map[string]string
	unforwarded     []string
	stop            context.CancelFunc
	ended           chan error
}

func (f *Forwards) Close() error {
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

func (f *Forwards) Bindings(app string) map[string]string {
	if f == nil {
		return nil
	}
	return f.bindings[app]
}

func (f *Forwards) BindingProxyEnv(app string) map[string]string {
	if f == nil {
		return nil
	}
	return f.bindingProxyEnv[app]
}

func (f *Forwards) Apps() []string {
	if f == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(f.bindings))
}

func (f *Forwards) Unforwarded() []string {
	if f == nil {
		return nil
	}
	return f.unforwarded
}

func ListDeclared(uses []Use) []string {
	var declared []string
	for _, use := range uses {
		if !slices.Contains(declared, use.Declared) {
			declared = append(declared, use.Declared)
		}
	}
	slices.Sort(declared)
	return declared
}

func listGrants(uses []Use) []*contractv1.ForwardPortsGrant {
	bound := map[string][]string{}
	for _, use := range uses {
		if !slices.Contains(bound[use.App], use.Bound) {
			bound[use.App] = append(bound[use.App], use.Bound)
		}
	}
	grants := make([]*contractv1.ForwardPortsGrant, 0, len(bound))
	for _, app := range slices.Sorted(maps.Keys(bound)) {
		grants = append(grants, &contractv1.ForwardPortsGrant{Grantee: app, Bindings: slices.Sorted(slices.Values(bound[app]))})
	}
	return grants
}

func FindBound(infra *contractv1.Manifest, kind resourcesv1.ResourceType, name string) (string, bool) {
	for _, resource := range infra.GetResources() {
		declared := resource.GetResource()
		if declared.GetType() == kind && declared.GetName() == name && resource.GetBinding() == "" {
			return resource.GetLogicalName(), true
		}
	}
	return "", false
}

func ListWholeProjectUses(infra *contractv1.Manifest) []Use {
	var uses []Use
	for _, resource := range infra.GetResources() {
		declared := resource.GetResource()
		if _, bindable := naming.BindableAs(declared.GetType()); !bindable || resource.GetBinding() != "" {
			continue
		}
		uses = append(uses, Use{App: WholeProject, Resource: declared.GetType(), Declared: declared.GetName(), Bound: resource.GetLogicalName()})
	}
	return uses
}

func RefuseUnforwarded(subject string, declared []string, err error) error {
	return fmt.Errorf("%s needs the bindings of %s, and the provider forwards no port to them: %s", subject, english.And(declared), readRefusalMessage(err))
}

func DescribeUnforwarded(subject string, unforwarded []string) string {
	return fmt.Sprintf("%s goes without the bindings of %s, since the provider forwards no port to them", subject, english.And(unforwarded))
}

func DescribeRefused(subject string, declared []string, err error) string {
	return fmt.Sprintf("%s goes without the bindings of %s: %s", subject, english.And(declared), readRefusalMessage(err))
}

func readRefusalMessage(err error) string {
	var rpcErr *connect.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Message()
	}
	return err.Error()
}

func Open(ctx context.Context, p *providerprocess.Provider, slug string, env *environmentv1.Environment, uses []Use, said *run.Span) (*Forwards, error) {
	req := &contractv1.ForwardPortsRequest{Slug: slug, Environment: env, Grants: listGrants(uses)}
	streamCtx, stop := context.WithCancel(ctx)
	answered := make(chan *contractv1.ForwardPortsResponse, 1)
	ended := make(chan error, 1)
	go func() {
		ended <- providerprocess.ForwardPorts(streamCtx, p, req, func(resp *contractv1.ForwardPortsResponse) {
			select {
			case answered <- resp:
			default:
			}
		}, func(event *progressv1.OperationEvent) { sayOn(said, event) })
	}()
	forwards := &Forwards{stop: stop, ended: ended}
	resp, err := awaitAnswer(answered, ended)
	if err != nil {
		stop()
		return nil, err
	}
	forwards.unforwarded = declaredNames(uses, resp.GetUnforwarded())
	byApp, err := liveBindings(uses, resp.GetBindings())
	if err != nil {
		return nil, errors.Join(err, forwards.Close())
	}
	forwards.bindings = byApp
	forwards.bindingProxyEnv = map[string]map[string]string{}
	for _, proxy := range resp.GetBindingProxies() {
		forwards.bindingProxyEnv[proxy.GetGrantee()] = map[string]string{
			processenv.RuntimeAddressEnvVar: proxy.GetAddress(),
			localrpc.SessionTokenEnvVar:     proxy.GetSessionToken(),
		}
	}
	return forwards, nil
}

func sayOn(span *run.Span, event *progressv1.OperationEvent) {
	switch event.GetLevel() {
	case progressv1.Level_LEVEL_WARN:
		span.Warn(event.GetMessage())
	case progressv1.Level_LEVEL_ERROR:
		span.Error(event.GetMessage())
	case progressv1.Level_LEVEL_DEBUG:
		span.Debug(event.GetMessage())
	default:
		span.Say(event.GetMessage())
	}
}

func declaredNames(uses []Use, bound []string) []string {
	var declared []string
	for _, use := range uses {
		if slices.Contains(bound, use.Bound) && !slices.Contains(declared, use.Declared) {
			declared = append(declared, use.Declared)
		}
	}
	slices.Sort(declared)
	return declared
}

func awaitAnswer(answered <-chan *contractv1.ForwardPortsResponse, ended chan error) (*contractv1.ForwardPortsResponse, error) {
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

func liveBindings(uses []Use, forwarded []*bindingsv1.Binding) (map[string]map[string]string, error) {
	byApp := map[string]map[string]string{}
	for _, use := range uses {
		at := slices.IndexFunc(forwarded, func(binding *bindingsv1.Binding) bool { return binding.GetName() == use.Bound })
		if at < 0 {
			continue
		}
		named := proto.Clone(forwarded[at]).(*bindingsv1.Binding)
		named.Name = use.Declared
		encoded, err := binding.Encode(use.Resource, named)
		if err != nil {
			return nil, fmt.Errorf("encode the binding of %s: %w", use.Declared, err)
		}
		if byApp[use.App] == nil {
			byApp[use.App] = map[string]string{}
		}
		maps.Copy(byApp[use.App], encoded.Env)
	}
	return byApp, nil
}
