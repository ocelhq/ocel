package apigateway

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
		return nil, false, fmt.Errorf("the none edge routes %s by rule, and it was built without an API Gateway v2 client to write those rules with", domain)
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

func putHostRule(ctx context.Context, c Clients, domain, host, api string, priority int32) error {
	for attempt := 1; ; attempt++ {
		rules, found, err := routingRules(ctx, c, domain)
		if err != nil {
			return err
		}
		if !found {
			return missingWildcardError(domain, host, api)
		}
		held, taken := hostRuleAmong(rules, host)
		if held != nil {
			return replaceHostRule(ctx, c, domain, host, api, priority, *held)
		}
		want := priority
		if want == 0 {
			want = freePriority(host, taken)
		}
		if want == 0 {
			return crowdedWildcardError(domain, host, api)
		}
		err = createHostRule(ctx, c, domain, host, api, want)
		var conflict *agv2types.ConflictException
		switch {
		case err == nil:
			return nil
		case priority == 0 && errors.As(err, &conflict) && attempt < hostRuleAttempts:
		default:
			return routeError(domain, host, api, err)
		}
	}
}

func hostRuleAmong(rules []agv2types.RoutingRule, host string) (*agv2types.RoutingRule, map[int32]bool) {
	taken := make(map[int32]bool, len(rules))
	var held *agv2types.RoutingRule
	for i, rule := range rules {
		if ruleHost(rule) == host {
			held = &rules[i]
			continue
		}
		taken[aws.ToInt32(rule.Priority)] = true
	}
	return held, taken
}

func replaceHostRule(ctx context.Context, c Clients, domain, host, api string, priority int32, held agv2types.RoutingRule) error {
	if priority == 0 {
		priority = aws.ToInt32(held.Priority)
	}
	if ruleTarget(held) == api && aws.ToInt32(held.Priority) == priority {
		return nil
	}
	if _, err := c.Routing.PutRoutingRule(ctx, &apigatewayv2.PutRoutingRuleInput{
		DomainName:    aws.String(domain),
		RoutingRuleId: held.RoutingRuleId,
		Priority:      aws.Int32(priority),
		Conditions:    hostCondition(host),
		Actions:       invokeAction(api),
	}); err != nil {
		return routeError(domain, host, api, err)
	}
	return nil
}

func createHostRule(ctx context.Context, c Clients, domain, host, api string, priority int32) error {
	_, err := c.Routing.CreateRoutingRule(ctx, &apigatewayv2.CreateRoutingRuleInput{
		DomainName: aws.String(domain),
		Priority:   aws.Int32(priority),
		Conditions: hostCondition(host),
		Actions:    invokeAction(api),
	})
	return err
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

func deleteHostRule(ctx context.Context, c Clients, domain, host string) error {
	return deleteRulesMatching(ctx, c, domain, func(held string) bool { return held == host })
}

func deleteLabelledRules(ctx context.Context, c Clients, domain, prefix, suffix string) error {
	return deleteRulesMatching(ctx, c, domain, func(held string) bool {
		label, ok := strings.CutSuffix(held, suffix)
		return ok && strings.HasPrefix(label, prefix)
	})
}

func deleteRulesMatching(ctx context.Context, c Clients, domain string, match func(string) bool) error {
	rules, found, err := routingRules(ctx, c, domain)
	if err != nil || !found {
		return err
	}
	var errs []error
	for _, rule := range rules {
		host := ruleHost(rule)
		if !match(host) {
			continue
		}
		if err := deleteRule(ctx, c, domain, aws.ToString(rule.RoutingRuleId), host); err != nil {
			errs = append(errs, err)
		}
	}
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
