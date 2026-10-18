package apigateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
)

type fakeRouting struct {
	g *fakeGateway
}

func (f *fakeRouting) ListRoutingRules(_ context.Context, in *apigatewayv2.ListRoutingRulesInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.ListRoutingRulesOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	name := aws.ToString(in.DomainName)
	f.g.record("ListRoutingRules " + name)
	domain, ok := f.g.domains[name]
	if !ok {
		return nil, &agv2types.NotFoundException{Message: aws.String("no domain " + name)}
	}
	items := make([]agv2types.RoutingRule, 0, len(domain.rules))
	for _, id := range slices.Sorted(maps.Keys(domain.rules)) {
		rule := domain.rules[id]
		items = append(items, agv2types.RoutingRule{
			RoutingRuleId: aws.String(rule.id),
			Priority:      aws.Int32(rule.priority),
			Conditions: []agv2types.RoutingRuleCondition{{
				MatchHeaders: &agv2types.RoutingRuleMatchHeaders{
					AnyOf: []agv2types.RoutingRuleMatchHeaderValue{{
						Header:    aws.String(rule.header),
						ValueGlob: aws.String(rule.host),
					}},
				},
			}},
			Actions: []agv2types.RoutingRuleAction{{
				InvokeApi: &agv2types.RoutingRuleActionInvokeApi{
					ApiId: aws.String(rule.api),
					Stage: aws.String(rule.stage),
				},
			}},
		})
	}
	items, token, err := page(items, in.NextToken, f.g.pageSize)
	if err != nil {
		return nil, err
	}
	return &apigatewayv2.ListRoutingRulesOutput{RoutingRules: items, NextToken: token}, nil
}

func hostConditionOf(conditions []agv2types.RoutingRuleCondition) (header, host string) {
	for _, condition := range conditions {
		if condition.MatchHeaders == nil {
			continue
		}
		for _, match := range condition.MatchHeaders.AnyOf {
			if strings.EqualFold(aws.ToString(match.Header), "host") {
				header, host = aws.ToString(match.Header), aws.ToString(match.ValueGlob)
			}
		}
	}
	return header, host
}

func checkRuleInput(domain *fakeDomain, host string, priority int32, conditions []agv2types.RoutingRuleCondition, actions []agv2types.RoutingRuleAction, self string) error {
	if host == "" {
		return fmt.Errorf("fake routing needs a host condition, got %+v", conditions)
	}
	if len(actions) != 1 || actions[0].InvokeApi == nil {
		return fmt.Errorf("fake routing serves only the invoke-api action, got %+v", actions)
	}
	if priority < 1 || priority > 1_000_000 {
		return fmt.Errorf("fake routing got priority %d, outside the 1-1,000,000 API Gateway allows", priority)
	}
	for _, rule := range domain.rules {
		if rule.priority == priority && rule.id != self {
			return &agv2types.ConflictException{Message: aws.String(fmt.Sprintf("priority %d is already taken by %s", priority, rule.id))}
		}
	}
	return nil
}

func (f *fakeRouting) CreateRoutingRule(_ context.Context, in *apigatewayv2.CreateRoutingRuleInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.CreateRoutingRuleOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	name := aws.ToString(in.DomainName)
	header, host := hostConditionOf(in.Conditions)
	f.g.record("CreateRoutingRule " + name + " " + host)
	domain, ok := f.g.domains[name]
	if !ok {
		return nil, &agv2types.NotFoundException{Message: aws.String("no domain " + name)}
	}
	priority := aws.ToInt32(in.Priority)
	if f.g.beforeRule != nil {
		f.g.beforeRule(f.g, priority)
	}
	if err := checkRuleInput(domain, host, priority, in.Conditions, in.Actions, ""); err != nil {
		return nil, err
	}
	f.g.next++
	id := "rule" + strconv.Itoa(f.g.next)
	domain.rules[id] = &fakeRule{
		id:       id,
		priority: priority,
		header:   header,
		host:     host,
		api:      aws.ToString(in.Actions[0].InvokeApi.ApiId),
		stage:    aws.ToString(in.Actions[0].InvokeApi.Stage),
	}
	return &apigatewayv2.CreateRoutingRuleOutput{RoutingRuleId: aws.String(id)}, nil
}

func (f *fakeRouting) PutRoutingRule(_ context.Context, in *apigatewayv2.PutRoutingRuleInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.PutRoutingRuleOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	name, id := aws.ToString(in.DomainName), aws.ToString(in.RoutingRuleId)
	header, host := hostConditionOf(in.Conditions)
	f.g.record("PutRoutingRule " + name + " " + host)
	domain, ok := f.g.domains[name]
	if !ok {
		return nil, &agv2types.NotFoundException{Message: aws.String("no domain " + name)}
	}
	rule, ok := domain.rules[id]
	if !ok {
		return nil, &agv2types.NotFoundException{Message: aws.String("no rule " + id)}
	}
	priority := aws.ToInt32(in.Priority)
	if err := checkRuleInput(domain, host, priority, in.Conditions, in.Actions, id); err != nil {
		return nil, err
	}
	rule.priority, rule.header, rule.host = priority, header, host
	rule.api, rule.stage = aws.ToString(in.Actions[0].InvokeApi.ApiId), aws.ToString(in.Actions[0].InvokeApi.Stage)
	return &apigatewayv2.PutRoutingRuleOutput{RoutingRuleId: aws.String(id)}, nil
}

func (f *fakeRouting) DeleteRoutingRule(_ context.Context, in *apigatewayv2.DeleteRoutingRuleInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.DeleteRoutingRuleOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	name, id := aws.ToString(in.DomainName), aws.ToString(in.RoutingRuleId)
	domain, ok := f.g.domains[name]
	if !ok {
		f.g.record("DeleteRoutingRule " + name + " " + id)
		return nil, &agv2types.NotFoundException{Message: aws.String("no domain " + name)}
	}
	rule, ok := domain.rules[id]
	if !ok {
		f.g.record("DeleteRoutingRule " + name + " " + id)
		return nil, &agv2types.NotFoundException{Message: aws.String("no rule " + id)}
	}
	f.g.record("DeleteRoutingRule " + name + " " + rule.host)
	delete(domain.rules, id)
	return &apigatewayv2.DeleteRoutingRuleOutput{}, nil
}
