package providerserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

type openForwards struct {
	mutex  sync.Mutex
	open   int
	closed chan struct{}
}

func (o *openForwards) hold() func() {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if o.open == 0 {
		o.closed = make(chan struct{})
	}
	o.open++
	return func() {
		o.mutex.Lock()
		defer o.mutex.Unlock()
		o.open--
		if o.open == 0 {
			close(o.closed)
		}
	}
}

func (o *openForwards) awaitClosed(ctx context.Context) error {
	o.mutex.Lock()
	if o.open == 0 {
		o.mutex.Unlock()
		return nil
	}
	closed := o.closed
	o.mutex.Unlock()
	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type streamLog struct {
	mutex  sync.Mutex
	stream *connect.ServerStream[contractv1.ForwardPortsEvent]
}

func (l *streamLog) send(event *contractv1.ForwardPortsEvent) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if l.stream == nil {
		return nil
	}
	return l.stream.Send(event)
}

func (l *streamLog) close() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.stream = nil
}

func (l *streamLog) say(event *progressv1.OperationEvent) {
	event.Time = timestamppb.Now()
	if event.GetLevel() == progressv1.Level_LEVEL_UNSPECIFIED {
		event.Level = progressv1.Level_LEVEL_INFO
	}
	_ = l.send(&contractv1.ForwardPortsEvent{Body: &contractv1.ForwardPortsEvent_Progress{Progress: event}})
}

func (l *streamLog) Say(message string) {
	l.say(&progressv1.OperationEvent{Message: progress.SanitizeMessage(message)})
}

func (l *streamLog) Warn(message string) {
	l.say(&progressv1.OperationEvent{Level: progressv1.Level_LEVEL_WARN, Message: progress.SanitizeMessage(message)})
}

func (l *streamLog) Error(message string) {
	l.say(&progressv1.OperationEvent{Level: progressv1.Level_LEVEL_ERROR, Message: progress.SanitizeMessage(message)})
}

func (l *streamLog) Detail(message string) {
	l.say(outputEvent(progressv1.Level_LEVEL_INFO, progress.SanitizeMessage(message)))
}

func (l *streamLog) Debug(line string) {
	l.say(outputEvent(progressv1.Level_LEVEL_DEBUG, progress.SanitizeMessage(line)))
}

func (l *streamLog) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

func (h *handlers) ForwardPorts(ctx context.Context, req *contractv1.ForwardPortsRequest, stream *connect.ServerStream[contractv1.ForwardPortsEvent]) error {
	opened := &openedForwards{release: h.forwards.hold(), failed: make(chan error, 1)}
	defer opened.close()
	p, err := h.session.use()
	if err != nil {
		return err
	}
	forwarding, err := decodeForwardPorts(req)
	if err != nil {
		return err
	}
	said := &streamLog{stream: stream}
	defer said.close()
	if err := opened.open(ctx, p, forwarding, said); err != nil {
		return provider.RefusalError(err)
	}
	resp, err := forwardedResponse(opened.bindings, opened.forwards, opened.proxied, opened.proxy)
	if err != nil {
		return provider.RefusalError(err)
	}
	if err := said.send(&contractv1.ForwardPortsEvent{Body: &contractv1.ForwardPortsEvent_Response{Response: resp}}); err != nil {
		return err
	}
	return opened.hold(ctx)
}

type portForwarding struct {
	spec   provider.DeploySpec
	names  []string
	grants []*contractv1.ForwardPortsGrant
}

func decodeForwardPorts(req *contractv1.ForwardPortsRequest) (portForwarding, error) {
	tier, err := decodeTier(req.GetEnvironment().GetTier())
	if err != nil {
		return portForwarding{}, err
	}
	env, err := envName(req.GetEnvironment())
	if err != nil {
		return portForwarding{}, provider.RefusalError(err)
	}
	names, err := listGrantedNames(req.GetGrants())
	if err != nil {
		return portForwarding{}, provider.RefusalError(err)
	}
	return portForwarding{spec: provider.DeploySpec{Slug: req.GetSlug(), Tier: tier, Env: env}, names: names, grants: req.GetGrants()}, nil
}

type openedForwards struct {
	release func()
	failed  chan error

	bindings []provider.Binding
	forwards []provider.PortForward
	proxied  []provider.Binding
	proxy    *provider.BindingProxy

	closeInBackground bool
}

func (o *openedForwards) open(ctx context.Context, p provider.Provider, forwarding portForwarding, said progress.Log) error {
	bindings, err := readPublishedBindings(ctx, p, forwarding.spec, forwarding.names)
	if err != nil {
		return err
	}
	o.bindings = bindings
	access := p.Hooks().BindingAccess
	reachable := slices.DeleteFunc(slices.Clone(bindings), func(binding provider.Binding) bool {
		return access == nil || !isReachableByPort(binding)
	})
	proxied := slices.DeleteFunc(slices.Clone(bindings), func(binding provider.Binding) bool {
		return access == nil || !isReachableByProxy(binding)
	})
	spec := forwarding.spec
	if len(reachable) > 0 {
		if o.forwards, err = access.ForwardPorts(ctx, provider.PortForwardRequest{Tier: spec.Tier, Bindings: reachable, ReportFailure: o.reportFailure}, said); err != nil {
			return err
		}
	}
	if len(proxied) > 0 {
		served, err := access.ServeProxy(ctx, provider.BindingProxyRequest{Slug: spec.Slug, Tier: spec.Tier, Env: spec.Env, Grants: listProxiedGrants(forwarding.grants, proxied), ReportFailure: o.reportFailure}, said)
		if err != nil {
			return err
		}
		if served.Address == "" {
			closeProxy(&served)
			proxied = nil
		} else {
			o.proxy = &served
		}
	}
	o.proxied = proxied
	return nil
}

func (o *openedForwards) reportFailure(err error) {
	select {
	case o.failed <- err:
	default:
	}
}

func (o *openedForwards) hold(ctx context.Context) error {
	if len(o.forwards) == 0 && o.proxy == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-o.failed:
		o.closeInBackground = true
		return provider.RefusalError(err)
	}
}

func (o *openedForwards) close() {
	if o.closeInBackground {
		go func() {
			defer o.release()
			closeForwards(o.forwards)
			closeProxy(o.proxy)
		}()
		return
	}
	defer o.release()
	closeForwards(o.forwards)
	closeProxy(o.proxy)
}

func listGrantedNames(grants []*contractv1.ForwardPortsGrant) ([]string, error) {
	var grantees, names []string
	for _, grant := range grants {
		if slices.Contains(grantees, grant.GetGrantee()) {
			return nil, refusal.Refuse(refusal.CodeInvalid, "more than one grant names the grantee %q", grant.GetGrantee())
		}
		grantees = append(grantees, grant.GetGrantee())
		for _, name := range grant.GetBindings() {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names, nil
}

func listProxiedGrants(grants []*contractv1.ForwardPortsGrant, proxied []provider.Binding) []provider.BindingGrant {
	var granted []provider.BindingGrant
	for _, grant := range grants {
		var bound []provider.Binding
		for _, binding := range proxied {
			if slices.Contains(grant.GetBindings(), binding.Name) {
				bound = append(bound, binding)
			}
		}
		if len(bound) > 0 {
			granted = append(granted, provider.BindingGrant{Grantee: grant.GetGrantee(), Bindings: bound})
		}
	}
	return granted
}

func isReachableByPort(binding provider.Binding) bool {
	switch binding.Type {
	case provider.BindingKV:
		return true
	case provider.BindingPostgres:
		return binding.Properties[provider.PropertyURL] == ""
	}
	return false
}

func isReachableByProxy(binding provider.Binding) bool {
	switch binding.Type {
	case provider.BindingBucket:
		return !binding.Endpointed()
	case provider.BindingTopic, provider.BindingTask, provider.BindingRealtime:
		return true
	}
	return false
}

func closeProxy(proxy *provider.BindingProxy) {
	if proxy != nil && proxy.Close != nil {
		proxy.Close()
	}
}

func closeForwards(forwards []provider.PortForward) {
	for _, forward := range forwards {
		if forward.Close != nil {
			forward.Close()
		}
	}
}

func readPublishedBindings(ctx context.Context, p provider.Provider, spec provider.DeploySpec, names []string) ([]provider.Binding, error) {
	store := variablestore.Store{KeyValues: p.KeyValues(), Cipher: p.Cipher()}
	scope := variablestore.Scope{Project: spec.Slug, Tier: spec.Tier}
	bindings := make([]provider.Binding, 0, len(names))
	for _, name := range names {
		published, err := store.ResolveBinding(ctx, scope, bindingEnvironment(spec), name)
		if errors.Is(err, variablestore.ErrNotPublished) {
			return nil, refusal.Refuse(refusal.CodeNotReady,
				"%s is not deployed yet: nothing is published under %s, so no port forwards to it", spec.Slug, name)
		}
		if err != nil {
			return nil, err
		}
		binding, err := bindingPublished(name, published)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func forwardedResponse(bindings []provider.Binding, forwards []provider.PortForward, proxied []provider.Binding, proxy *provider.BindingProxy) (*contractv1.ForwardPortsResponse, error) {
	resp := &contractv1.ForwardPortsResponse{}
	if proxy != nil {
		for _, session := range proxy.Sessions {
			resp.BindingProxies = append(resp.BindingProxies, &contractv1.BindingProxy{Grantee: session.Grantee, Address: proxy.Address, SessionToken: session.SessionToken})
		}
	}
	for _, binding := range bindings {
		if slices.ContainsFunc(proxied, func(served provider.Binding) bool { return served.Name == binding.Name }) && !slices.Contains(proxy.Unserved, binding.Name) {
			message, err := provider.BindingMessage(binding)
			if err != nil {
				return nil, err
			}
			resp.Bindings = append(resp.Bindings, message)
			continue
		}
		at := slices.IndexFunc(forwards, func(forward provider.PortForward) bool { return forward.Binding == binding.Name })
		if at < 0 {
			resp.Unforwarded = append(resp.Unforwarded, binding.Name)
			continue
		}
		forwarded, err := forwardedBinding(binding, forwards[at])
		if err != nil {
			return nil, err
		}
		message, err := provider.BindingMessage(forwarded)
		if err != nil {
			return nil, err
		}
		resp.Bindings = append(resp.Bindings, message)
	}
	return resp, nil
}

func forwardedBinding(binding provider.Binding, forward provider.PortForward) (provider.Binding, error) {
	host, port, err := net.SplitHostPort(forward.LocalAddress)
	if err != nil {
		return provider.Binding{}, fmt.Errorf("the forward of %s listens on %q, which is no host and port: %w", binding.Name, forward.LocalAddress, err)
	}
	properties := maps.Clone(binding.Properties)
	maps.Copy(properties, forward.Properties)
	properties[provider.PropertyTLSServerName] = cmp.Or(properties[provider.PropertyTLSServerName], properties[provider.PropertyHost])
	properties[provider.PropertyHost], properties[provider.PropertyPort] = host, port
	binding.Properties = properties
	if binding.Type == provider.BindingPostgres && properties[provider.PropertyPassword] == "" {
		return provider.Binding{}, fmt.Errorf("the forward of %s came back without the password it names by secret %q, and a client connecting through it has no password to give",
			binding.Name, binding.PasswordSecret())
	}
	return binding, nil
}
