package apigateway

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

	"github.com/ocelhq/ocel/pkg/edge"
)

const (
	hostHeader = "host"
	anyHost    = "*"

	catchAllPriority int32 = 1_000_000
	hostRuleFloor    int32 = 1
	hostRuleCeiling  int32 = catchAllPriority - hostRuleFloor

	hostRuleAttempts = 5
)

func routingRules(ctx context.Context, c Clients, domain string) ([]agv2types.RoutingRule, bool, error) {
	if c.Routing == nil {
		return nil, false, fmt.Errorf("the %q edge routes %s by rule, and it was built without an API Gateway v2 client to write those rules with", Kind, domain)
	}
	var (
		out   []agv2types.RoutingRule
		token *string
	)
	for {
		page, err := c.Routing.ListRoutingRules(ctx, &apigatewayv2.ListRoutingRulesInput{
			DomainName: aws.String(domain),
			NextToken:  token,
		})
		if err != nil {
			if isNotFound(err) {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("read the routing rules on %s: %w", domain, err)
		}
		out = append(out, page.RoutingRules...)
		if aws.ToString(page.NextToken) == "" {
			return out, true, nil
		}
		token = page.NextToken
	}
}

func catchAllOwner(ctx context.Context, c Clients, domain string) (string, error) {
	rules, found, err := routingRules(ctx, c, domain)
	if err != nil || !found {
		return "", err
	}
	for _, rule := range rules {
		if ruleHost(rule) == anyHost {
			return edge.PreviewEntryOwner, nil
		}
	}
	return "", nil
}

func ruleHost(rule agv2types.RoutingRule) string {
	for _, condition := range rule.Conditions {
		if condition.MatchHeaders == nil {
			continue
		}
		for _, header := range condition.MatchHeaders.AnyOf {
			if strings.EqualFold(aws.ToString(header.Header), hostHeader) {
				return aws.ToString(header.ValueGlob)
			}
		}
	}
	return ""
}

func ruleTarget(rule agv2types.RoutingRule) string {
	for _, action := range rule.Actions {
		if action.InvokeApi == nil {
			continue
		}
		return aws.ToString(action.InvokeApi.ApiId)
	}
	return ""
}

type listedRules struct {
	c      Clients
	domain string
	rules  []agv2types.RoutingRule
	found  bool
}

func listRules(ctx context.Context, c Clients, domain string) (*listedRules, error) {
	listed := &listedRules{c: c, domain: domain}
	return listed, listed.relist(ctx)
}

func (l *listedRules) relist(ctx context.Context) error {
	rules, found, err := routingRules(ctx, l.c, l.domain)
	if err != nil {
		return err
	}
	l.rules, l.found = rules, found
	return nil
}

func putHostRule(ctx context.Context, c Clients, domain, host, api string, priority int32) error {
	listed, err := listRules(ctx, c, domain)
	if err != nil {
		return err
	}
	return listed.putHostRule(ctx, host, api, priority)
}

func (l *listedRules) putHostRule(ctx context.Context, host, api string, priority int32) error {
	for attempt := 1; ; attempt++ {
		if !l.found {
			return missingWildcardError(l.domain, host, api)
		}
		existing, taken := hostRuleAmong(l.rules, host)
		if existing != nil {
			return replaceHostRule(ctx, l.c, l.domain, host, api, priority, existing)
		}
		want := priority
		if want == 0 {
			want = freePriority(host, taken)
		}
		if want == 0 {
			return crowdedWildcardError(l.domain, host, api)
		}
		created, err := createHostRule(ctx, l.c, l.domain, host, api, want)
		var conflict *agv2types.ConflictException
		switch {
		case err == nil:
			l.rules = append(l.rules, agv2types.RoutingRule{RoutingRuleId: created, Priority: aws.Int32(want), Conditions: hostCondition(host), Actions: invokeAction(api)})
			return nil
		case priority == 0 && errors.As(err, &conflict) && attempt < hostRuleAttempts:
			if err := l.relist(ctx); err != nil {
				return err
			}
		default:
			return routeError(l.domain, host, api, err)
		}
	}
}

func hostRuleAmong(rules []agv2types.RoutingRule, host string) (*agv2types.RoutingRule, map[int32]bool) {
	taken := make(map[int32]bool, len(rules))
	var existing *agv2types.RoutingRule
	for i, rule := range rules {
		if ruleHost(rule) == host {
			existing = &rules[i]
			continue
		}
		taken[aws.ToInt32(rule.Priority)] = true
	}
	return existing, taken
}

func replaceHostRule(ctx context.Context, c Clients, domain, host, api string, priority int32, existing *agv2types.RoutingRule) error {
	if priority == 0 {
		priority = aws.ToInt32(existing.Priority)
	}
	if ruleTarget(*existing) == api && aws.ToInt32(existing.Priority) == priority {
		return nil
	}
	if _, err := c.Routing.PutRoutingRule(ctx, &apigatewayv2.PutRoutingRuleInput{
		DomainName:    aws.String(domain),
		RoutingRuleId: existing.RoutingRuleId,
		Priority:      aws.Int32(priority),
		Conditions:    hostCondition(host),
		Actions:       invokeAction(api),
	}); err != nil {
		return routeError(domain, host, api, err)
	}
	existing.Priority, existing.Conditions, existing.Actions = aws.Int32(priority), hostCondition(host), invokeAction(api)
	return nil
}

func createHostRule(ctx context.Context, c Clients, domain, host, api string, priority int32) (*string, error) {
	created, err := c.Routing.CreateRoutingRule(ctx, &apigatewayv2.CreateRoutingRuleInput{
		DomainName: aws.String(domain),
		Priority:   aws.Int32(priority),
		Conditions: hostCondition(host),
		Actions:    invokeAction(api),
	})
	if err != nil {
		return nil, err
	}
	return created.RoutingRuleId, nil
}

func hostCondition(host string) []agv2types.RoutingRuleCondition {
	return []agv2types.RoutingRuleCondition{{
		MatchHeaders: &agv2types.RoutingRuleMatchHeaders{
			AnyOf: []agv2types.RoutingRuleMatchHeaderValue{{
				Header:    aws.String(hostHeader),
				ValueGlob: aws.String(host),
			}},
		},
	}}
}

func invokeAction(api string) []agv2types.RoutingRuleAction {
	return []agv2types.RoutingRuleAction{{
		InvokeApi: &agv2types.RoutingRuleActionInvokeApi{
			ApiId: aws.String(api),
			Stage: aws.String(stageName),
		},
	}}
}

func deleteHostRules(ctx context.Context, c Clients, domain string, hosts []string) error {
	listed, err := listRules(ctx, c, domain)
	if err != nil {
		return err
	}
	return listed.deleteHostRules(ctx, hosts)
}

func (l *listedRules) deleteHostRules(ctx context.Context, hosts []string) error {
	return l.deleteRulesMatching(ctx, func(name string) bool { return slices.Contains(hosts, name) })
}

func deleteRulesMatching(ctx context.Context, c Clients, domain string, match func(string) bool) error {
	listed, err := listRules(ctx, c, domain)
	if err != nil {
		return err
	}
	return listed.deleteRulesMatching(ctx, match)
}

func (l *listedRules) deleteRulesMatching(ctx context.Context, match func(string) bool) error {
	var errs []error
	l.rules = slices.DeleteFunc(l.rules, func(rule agv2types.RoutingRule) bool {
		host := ruleHost(rule)
		if !match(host) {
			return false
		}
		if err := deleteRule(ctx, l.c, l.domain, aws.ToString(rule.RoutingRuleId), host); err != nil {
			errs = append(errs, err)
			return false
		}
		return true
	})
	return errors.Join(errs...)
}

func deleteRule(ctx context.Context, c Clients, domain, id, host string) error {
	if _, err := c.Routing.DeleteRoutingRule(ctx, &apigatewayv2.DeleteRoutingRuleInput{
		DomainName:    aws.String(domain),
		RoutingRuleId: aws.String(id),
	}); err != nil && !isNotFound(err) {
		return fmt.Errorf("stop routing %s on %s: %w", host, domain, err)
	}
	return nil
}

func freePriority(host string, taken map[int32]bool) int32 {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(host))
	start := int32(sum.Sum32() % uint32(hostRuleCeiling))
	for step := int32(0); step < hostRuleCeiling; step++ {
		priority := hostRuleFloor + (start+step)%hostRuleCeiling
		if !taken[priority] {
			return priority
		}
	}
	return 0
}
