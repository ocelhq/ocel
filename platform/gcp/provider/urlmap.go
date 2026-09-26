package gcp

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func matcherFor(hostname string) string { return "host-" + naming.Sanitize(hostname) }

func (p *Provider) Route(ctx context.Context, urlMap, hostname, backend string) error {
	if urlMap == "" || hostname == "" || backend == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"route %q through url map %q onto backend %q: a host rule names all three", hostname, urlMap, backend)
	}
	return p.rewrite(ctx, urlMap, "route "+hostname+" through the load balancer", func(clients *clients, current *compute.UrlMap) bool {
		return routed(current, hostname, clients.backendLink(backend))
	})
}

func (p *Provider) ServeNotFound(ctx context.Context, urlMap, hostname string) error {
	if urlMap == "" || hostname == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"serve a 404 for %q on url map %q until its app releases: a host rule names both", hostname, urlMap)
	}
	return p.rewrite(ctx, urlMap, "answer "+hostname+" with a 404 until its app has released", func(_ *clients, current *compute.UrlMap) bool {
		return unclaimed(current, hostname)
	})
}

func (p *Provider) Unroute(ctx context.Context, urlMap, hostname string) error {
	if urlMap == "" || hostname == "" {
		return nil
	}
	return p.rewrite(ctx, urlMap, "stop routing "+hostname+" through the load balancer", func(_ *clients, current *compute.UrlMap) bool {
		return unrouted(current, hostname)
	})
}

func (p *Provider) rewrite(ctx context.Context, urlMap, doing string, change func(*clients, *compute.UrlMap) bool) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	engine, err := clients.Compute()
	if err != nil {
		return err
	}
	return p.retryWrite(ctx, doing, func() error {
		current, err := attempted(ctx, func(call ...googleapi.CallOption) (*compute.UrlMap, error) {
			return engine.UrlMaps.Get(clients.project, urlMap).Context(ctx).Do(call...)
		})
		if err != nil {
			return fmt.Errorf("read the url map %s: %w", urlMap, err)
		}
		changed := change(clients, current)
		if !refreshed(current) && !changed {
			return nil
		}
		return p.runOperation(ctx, clients, engine, doing, func(call ...googleapi.CallOption) (*compute.Operation, error) {
			return engine.UrlMaps.Patch(clients.project, urlMap, &compute.UrlMap{
				Fingerprint:     current.Fingerprint,
				HostRules:       current.HostRules,
				PathMatchers:    current.PathMatchers,
				ForceSendFields: []string{"HostRules", "PathMatchers"},
			}).Context(ctx).Do(call...)
		})
	})
}

func refreshed(current *compute.UrlMap) bool {
	changed := false
	for _, matcher := range current.PathMatchers {
		if aborting(matcher.DefaultRouteAction) != 0 && matcher.DefaultService != current.DefaultService {
			matcher.DefaultService = current.DefaultService
			changed = true
		}
	}
	return changed
}

func routed(current *compute.UrlMap, hostname, backend string) bool {
	return matched(current, hostname, &compute.PathMatcher{Name: matcherFor(hostname), DefaultService: backend})
}

func unclaimed(current *compute.UrlMap, hostname string) bool {
	return matched(current, hostname, &compute.PathMatcher{
		Name:               matcherFor(hostname),
		DefaultService:     current.DefaultService,
		DefaultRouteAction: refusing(),
	})
}

func refusing() *compute.HttpRouteAction {
	return &compute.HttpRouteAction{FaultInjectionPolicy: &compute.HttpFaultInjection{
		Abort: &compute.HttpFaultAbort{HttpStatus: http.StatusNotFound, Percentage: 100},
	}}
}

func matched(current *compute.UrlMap, hostname string, want *compute.PathMatcher) bool {
	changed := false
	at := slices.IndexFunc(current.HostRules, func(rule *compute.HostRule) bool {
		return slices.Contains(rule.Hosts, hostname)
	})
	switch {
	case at < 0:
		current.HostRules = append(current.HostRules, &compute.HostRule{Hosts: []string{hostname}, PathMatcher: want.Name})
		changed = true
	case current.HostRules[at].PathMatcher != want.Name:
		current.HostRules[at].PathMatcher = want.Name
		changed = true
	}
	on := slices.IndexFunc(current.PathMatchers, func(path *compute.PathMatcher) bool { return path.Name == want.Name })
	switch {
	case on < 0:
		current.PathMatchers = append(current.PathMatchers, want)
		changed = true
	case !answers(current.PathMatchers[on], want):
		current.PathMatchers[on] = want
		changed = true
	}
	return changed
}

func answers(current, want *compute.PathMatcher) bool {
	return current.DefaultService == want.DefaultService && aborting(current.DefaultRouteAction) == aborting(want.DefaultRouteAction)
}

func aborting(action *compute.HttpRouteAction) int64 {
	if action == nil || action.FaultInjectionPolicy == nil || action.FaultInjectionPolicy.Abort == nil {
		return 0
	}
	return action.FaultInjectionPolicy.Abort.HttpStatus
}

func unrouted(current *compute.UrlMap, hostname string) bool {
	matcher := matcherFor(hostname)
	rules := slices.DeleteFunc(current.HostRules, func(rule *compute.HostRule) bool {
		return slices.Contains(rule.Hosts, hostname)
	})
	paths := slices.DeleteFunc(current.PathMatchers, func(path *compute.PathMatcher) bool {
		return path.Name == matcher
	})
	if len(rules) == len(current.HostRules) && len(paths) == len(current.PathMatchers) {
		return false
	}
	current.HostRules, current.PathMatchers = rules, paths
	return true
}

func (p *Provider) runOperation(
	ctx context.Context,
	clients *clients,
	engine *compute.Service,
	doing string,
	call func(...googleapi.CallOption) (*compute.Operation, error),
) error {
	started, err := attempted(ctx, call)
	if err != nil {
		return fmt.Errorf("ask Compute Engine to %s: %w", doing, err)
	}
	finished, err := until(ctx, "Compute Engine to "+doing, func() (*compute.Operation, error) {
		if started.Status == operationDone {
			return started, nil
		}
		return attempted(ctx, func(opt ...googleapi.CallOption) (*compute.Operation, error) {
			return engine.GlobalOperations.Get(clients.project, started.Name).Context(ctx).Do(opt...)
		})
	}, func(op *compute.Operation) bool { return op != nil && op.Status == operationDone })
	if err != nil {
		return err
	}
	if finished.Error != nil && len(finished.Error.Errors) > 0 {
		return refusal.Refuse(refusal.CodeNotReady,
			"Compute Engine refused to %s: %s", doing, finished.Error.Errors[0].Message)
	}
	return nil
}

const operationDone = "DONE"
