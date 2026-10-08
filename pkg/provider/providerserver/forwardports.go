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
	release := h.forwards.hold()
	var forwards []provider.PortForward
	closeInBackground := false
	defer func() {
		if closeInBackground {
			go func() {
				defer release()
				closeForwards(forwards)
			}()
			return
		}
		defer release()
		closeForwards(forwards)
	}()
	p, err := h.session.use()
	if err != nil {
		return err
	}
	tier, err := decodeTier(req.GetEnvironment().GetTier())
	if err != nil {
		return err
	}
	env, err := envName(req.GetEnvironment())
	if err != nil {
		return provider.RefusalError(err)
	}
	spec := provider.DeploySpec{Slug: req.GetSlug(), Tier: tier, Env: env}
	bindings, err := readPublishedBindings(ctx, p, spec, req.GetBindings())
	if err != nil {
		return provider.RefusalError(err)
	}
	forward := p.Hooks().ForwardPorts
	reachable := slices.DeleteFunc(slices.Clone(bindings), func(binding provider.Binding) bool {
		return forward == nil || !isReachableByPort(binding)
	})
	said := &streamLog{stream: stream}
	defer said.close()
	failed := make(chan error, 1)
	reportFailure := func(err error) {
		select {
		case failed <- err:
		default:
		}
	}
	if len(reachable) > 0 {
		forwards, err = forward(ctx, provider.PortForwardRequest{Tier: tier, Bindings: reachable, ReportFailure: reportFailure}, said)
		if err != nil {
			return provider.RefusalError(err)
		}
	}
	resp, err := forwardedResponse(bindings, forwards)
	if err != nil {
		return provider.RefusalError(err)
	}
	if err := said.send(&contractv1.ForwardPortsEvent{Body: &contractv1.ForwardPortsEvent_Response{Response: resp}}); err != nil {
		return err
	}
	if len(forwards) == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-failed:
		closeInBackground = true
		return provider.RefusalError(err)
	}
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

func forwardedResponse(bindings []provider.Binding, forwards []provider.PortForward) (*contractv1.ForwardPortsResponse, error) {
	resp := &contractv1.ForwardPortsResponse{}
	for _, binding := range bindings {
		at := slices.IndexFunc(forwards, func(forward provider.PortForward) bool { return forward.Binding == binding.Name })
		if at < 0 {
			resp.Unforwarded = append(resp.Unforwarded, binding.Name)
			continue
		}
		forwarded, err := forwardedBinding(binding, forwards[at].LocalAddress)
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

func forwardedBinding(binding provider.Binding, localAddress string) (provider.Binding, error) {
	host, port, err := net.SplitHostPort(localAddress)
	if err != nil {
		return provider.Binding{}, fmt.Errorf("the forward of %s listens on %q, which is no host and port: %w", binding.Name, localAddress, err)
	}
	properties := maps.Clone(binding.Properties)
	properties[provider.PropertyTLSServerName] = cmp.Or(properties[provider.PropertyTLSServerName], properties[provider.PropertyHost])
	properties[provider.PropertyHost], properties[provider.PropertyPort] = host, port
	binding.Properties = properties
	return binding, nil
}
