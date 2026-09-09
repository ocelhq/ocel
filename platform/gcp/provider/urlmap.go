package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func matcherFor(hostname string) string { return "host-" + naming.Sanitize(hostname) }

func (p *Provider) Route(ctx context.Context, urlMap, hostname, backend string) error {
	if urlMap == "" || hostname == "" || backend == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"route %q through url map %q onto backend %q: a host rule names all three", hostname, urlMap, backend)
	}
	return p.rewrite(ctx, urlMap, "route "+hostname+" through the load balancer", func(held *compute.UrlMap) bool {
		return routed(held, hostname, p.backendLink(backend))
	})
}

func (p *Provider) Unroute(ctx context.Context, urlMap, hostname string) error {
	if urlMap == "" || hostname == "" {
		return nil
	}
	return p.rewrite(ctx, urlMap, "stop routing "+hostname+" through the load balancer", func(held *compute.UrlMap) bool {
		return unrouted(held, hostname)
	})
}

func (p *Provider) backendLink(backend string) string {
	if strings.Contains(backend, "/") {
		return backend
	}
	return "projects/" + p.options.Project + "/global/backendServices/" + backend
}

func (p *Provider) rewrite(ctx context.Context, urlMap, doing string, change func(*compute.UrlMap) bool) error {
	engine, err := p.clients.Compute()
	if err != nil {
		return err
	}
	return p.settled(ctx, doing, func() error {
		held, err := attempted(ctx, func(call ...googleapi.CallOption) (*compute.UrlMap, error) {
			return engine.UrlMaps.Get(p.options.Project, urlMap).Context(ctx).Do(call...)
		})
		if err != nil {
			return fmt.Errorf("read the url map %s: %w", urlMap, err)
		}
		if !change(held) {
			return nil
		}
		return p.settle(ctx, engine, doing, func(call ...googleapi.CallOption) (*compute.Operation, error) {
			return engine.UrlMaps.Patch(p.options.Project, urlMap, &compute.UrlMap{
				Fingerprint:  held.Fingerprint,
				HostRules:    held.HostRules,
				PathMatchers: held.PathMatchers,
			}).Context(ctx).Do(call...)
		})
	})
}

func routed(held *compute.UrlMap, hostname, backend string) bool {
	matcher := matcherFor(hostname)
	changed := false
	at := slices.IndexFunc(held.HostRules, func(rule *compute.HostRule) bool {
		return slices.Contains(rule.Hosts, hostname)
	})
	switch {
	case at < 0:
		held.HostRules = append(held.HostRules, &compute.HostRule{Hosts: []string{hostname}, PathMatcher: matcher})
		changed = true
	case held.HostRules[at].PathMatcher != matcher:
		held.HostRules[at].PathMatcher = matcher
		changed = true
	}
	on := slices.IndexFunc(held.PathMatchers, func(path *compute.PathMatcher) bool { return path.Name == matcher })
	switch {
	case on < 0:
		held.PathMatchers = append(held.PathMatchers, &compute.PathMatcher{Name: matcher, DefaultService: backend})
		changed = true
	case held.PathMatchers[on].DefaultService != backend:
		held.PathMatchers[on].DefaultService = backend
		changed = true
	}
	return changed
}

func unrouted(held *compute.UrlMap, hostname string) bool {
	matcher := matcherFor(hostname)
	rules := slices.DeleteFunc(held.HostRules, func(rule *compute.HostRule) bool {
		return slices.Contains(rule.Hosts, hostname)
	})
	paths := slices.DeleteFunc(held.PathMatchers, func(path *compute.PathMatcher) bool {
		return path.Name == matcher
	})
	if len(rules) == len(held.HostRules) && len(paths) == len(held.PathMatchers) {
		return false
	}
	held.HostRules, held.PathMatchers = rules, paths
	return true
}

func (p *Provider) settle(
	ctx context.Context,
	engine *compute.Service,
	doing string,
	call func(...googleapi.CallOption) (*compute.Operation, error),
) error {
	started, err := attempted(ctx, call)
	if err != nil {
		return fmt.Errorf("ask Compute Engine to %s: %w", doing, err)
	}
	settled, err := until(ctx, "Compute Engine to "+doing, func() (*compute.Operation, error) {
		if started.Status == operationDone {
			return started, nil
		}
		return attempted(ctx, func(opt ...googleapi.CallOption) (*compute.Operation, error) {
			return engine.GlobalOperations.Get(p.options.Project, started.Name).Context(ctx).Do(opt...)
		})
	}, func(op *compute.Operation) bool { return op != nil && op.Status == operationDone })
	if err != nil {
		return err
	}
	if settled.Error != nil && len(settled.Error.Errors) > 0 {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"Compute Engine refused to %s: %s", doing, settled.Error.Errors[0].Message)
	}
	return nil
}

const operationDone = "DONE"

var _ alb.Routes = (*Provider)(nil)
