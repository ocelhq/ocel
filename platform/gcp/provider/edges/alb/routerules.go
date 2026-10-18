package alb

import (
	"slices"
	"strings"

	"google.golang.org/api/compute/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func routeRulesOf(clients *gcloud.Clients, dynamic string, routes *StaticRoutes) []*compute.HttpRouteRule {
	if routes == nil {
		return nil
	}
	var rules []*compute.HttpRouteRule
	for _, prefix := range routes.Revalidate {
		if !slices.ContainsFunc(routes.Immutable, func(immutable string) bool { return strings.HasPrefix(prefix, immutable) }) {
			continue
		}
		rules = append(rules, &compute.HttpRouteRule{
			Priority:   int64(len(rules)),
			MatchRules: []*compute.HttpRouteRuleMatch{{PrefixMatch: prefix}},
			Service:    dynamic,
		})
	}
	for _, prefix := range routes.Immutable {
		rules = append(rules, &compute.HttpRouteRule{
			Priority:    int64(len(rules)),
			MatchRules:  []*compute.HttpRouteRuleMatch{{PrefixMatch: prefix}},
			Service:     clients.BucketLink(routes.Backend),
			RouteAction: &compute.HttpRouteAction{UrlRewrite: &compute.UrlRewrite{PathPrefixRewrite: routes.Objects + prefix}},
		})
	}
	return rules
}

func sameRouteRules(current, want []*compute.HttpRouteRule) bool {
	return slices.EqualFunc(current, want, func(a, b *compute.HttpRouteRule) bool {
		return a.Priority == b.Priority && sameLink(a.Service, b.Service) && prefixOf(a) == prefixOf(b) && rewriteOf(a) == rewriteOf(b)
	})
}

func prefixOf(rule *compute.HttpRouteRule) string {
	if len(rule.MatchRules) == 0 {
		return ""
	}
	return rule.MatchRules[0].PrefixMatch
}

func rewriteOf(rule *compute.HttpRouteRule) string {
	if rule.RouteAction == nil || rule.RouteAction.UrlRewrite == nil {
		return ""
	}
	return rule.RouteAction.UrlRewrite.PathPrefixRewrite
}

func sameLink(a, b string) bool { return relativeLink(a) == relativeLink(b) }

func relativeLink(link string) string {
	if at := strings.Index(link, "projects/"); at >= 0 {
		return link[at:]
	}
	return link
}
