package gcp

import (
	"slices"
	"strings"

	"google.golang.org/api/compute/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func staticRulesOf(clients *clients, dynamic string, files *alb.StaticRoutes) []*compute.HttpRouteRule {
	if files == nil || len(files.Immutable) == 0 {
		return nil
	}
	var rules []*compute.HttpRouteRule
	for _, prefix := range files.Revalidate {
		if !slices.ContainsFunc(files.Immutable, func(immutable string) bool { return strings.HasPrefix(prefix, immutable) }) {
			continue
		}
		rules = append(rules, &compute.HttpRouteRule{
			Priority:   int64(len(rules)),
			MatchRules: []*compute.HttpRouteRuleMatch{{PrefixMatch: prefix}},
			Service:    dynamic,
		})
	}
	for _, prefix := range files.Immutable {
		rules = append(rules, &compute.HttpRouteRule{
			Priority:    int64(len(rules)),
			MatchRules:  []*compute.HttpRouteRuleMatch{{PrefixMatch: prefix}},
			Service:     clients.bucketLink(files.Backend),
			RouteAction: &compute.HttpRouteAction{UrlRewrite: &compute.UrlRewrite{PathPrefixRewrite: files.Objects + prefix}},
		})
	}
	return rules
}

func sameRouteRules(current, want []*compute.HttpRouteRule) bool {
	return slices.EqualFunc(current, want, func(a, b *compute.HttpRouteRule) bool {
		return a.Priority == b.Priority && a.Service == b.Service && prefixOf(a) == prefixOf(b) && rewriteOf(a) == rewriteOf(b)
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
