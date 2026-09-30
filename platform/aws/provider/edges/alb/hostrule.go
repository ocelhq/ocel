package alb

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	hostRulePriorities = 40000
	hostRuleFloor      = 10000
	rulePlacements     = 8
	servicesPerRead    = 10

	unservedStatus      = "503"
	unservedContentType = "text/plain"
	unservedBody        = "no release of this app is promoted here yet"

	ruleQuota = "Rules per Application Load Balancer (L-7EED9B64)"
)

func buildForwardActions(group string) []elbv2types.Action {
	return []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: aws.String(group)}}
}

func buildUnservedActions() []elbv2types.Action {
	return []elbv2types.Action{{
		Type: elbv2types.ActionTypeEnumFixedResponse,
		FixedResponseConfig: &elbv2types.FixedResponseActionConfig{
			StatusCode:  aws.String(unservedStatus),
			ContentType: aws.String(unservedContentType),
			MessageBody: aws.String(unservedBody),
		},
	}}
}

func computeRulePriority(hostname string, attempt int) int32 {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(hostname))
	return int32(hostRuleFloor + (int(sum.Sum32()%hostRulePriorities)+attempt)%hostRulePriorities)
}

func (s *stack) placeRule(ctx context.Context, c Clients, listener, hostname string, actions []elbv2types.Action) (string, error) {
	for attempt := range rulePlacements {
		created, err := c.Balancers.CreateRule(ctx, &elbv2.CreateRuleInput{
			ListenerArn: aws.String(listener),
			Priority:    aws.Int32(computeRulePriority(hostname, attempt)),
			Conditions: []elbv2types.RuleCondition{{
				Field:            aws.String("host-header"),
				HostHeaderConfig: &elbv2types.HostHeaderConditionConfig{Values: []string{hostname}},
			}},
			Actions: actions,
			Tags: []elbv2types.Tag{
				{Key: aws.String("ocel:managed-by"), Value: aws.String("ocel")},
				{Key: aws.String("ocel:project"), Value: aws.String(s.state.Slug)},
			},
		})
		var taken *elbv2types.PriorityInUseException
		if errors.As(err, &taken) {
			continue
		}
		var full *elbv2types.TooManyRulesException
		if errors.As(err, &full) {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"the load balancer every container app behind Cloudflare in this tier shares holds as many rules as its quota allows, so %s cannot be routed: raise the quota %s in Service Quotas (Elastic Load Balancing), or remove a hostname or preview behind it, and deploy again",
				hostname, ruleQuota)
		}
		if err != nil {
			return "", fmt.Errorf("route %s on the load balancer: %w", hostname, err)
		}
		if len(created.Rules) == 0 {
			return "", fmt.Errorf("route %s on the load balancer: it answered no rule", hostname)
		}
		return aws.ToString(created.Rules[0].RuleArn), nil
	}
	return "", refusal.Refuse(refusal.CodeBusy,
		"every priority %s could take on the load balancer was held by another hostname, %d times over: remove a hostname behind it, or run this again", hostname, rulePlacements)
}

func (s *stack) hasRule(ctx context.Context, c Clients, rule string) (bool, error) {
	if rule == "" {
		return false, nil
	}
	read, err := c.Balancers.DescribeRules(ctx, &elbv2.DescribeRulesInput{RuleArns: []string{rule}})
	var gone *elbv2types.RuleNotFoundException
	switch {
	case errors.As(err, &gone):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the rule %s: %w", rule, err)
	}
	return len(read.Rules) > 0, nil
}

func readActions(ctx context.Context, c Clients, rule string) ([]elbv2types.Action, error) {
	read, err := c.Balancers.DescribeRules(ctx, &elbv2.DescribeRulesInput{RuleArns: []string{rule}})
	if err != nil {
		return nil, fmt.Errorf("read the rule %s: %w", rule, err)
	}
	if len(read.Rules) == 0 {
		return nil, fmt.Errorf("read the rule %s: it is gone", rule)
	}
	return read.Rules[0].Actions, nil
}

func readForwardedGroup(actions []elbv2types.Action) string {
	for _, action := range actions {
		if action.Type == elbv2types.ActionTypeEnumForward {
			return aws.ToString(action.TargetGroupArn)
		}
	}
	return ""
}

func (s *stack) forwardRuleTo(ctx context.Context, c Clients, rule, group string, refuseInactive router.StillActive) error {
	if err := refuseInactive(ctx); err != nil {
		return err
	}
	prior, err := readActions(ctx, c, rule)
	if err != nil {
		return router.Unserved{Err: err}
	}
	if _, err := c.Balancers.ModifyRule(ctx, &elbv2.ModifyRuleInput{RuleArn: aws.String(rule), Actions: buildForwardActions(group)}); err != nil {
		return router.Unserved{Err: fmt.Errorf("forward the rule %s to %s: %w", rule, group, err)}
	}
	inactive := refuseInactive(ctx)
	if inactive == nil {
		return nil
	}
	current, err := readActions(ctx, c, rule)
	if err != nil || readForwardedGroup(current) != group {
		return errors.Join(inactive, err)
	}
	if _, err := c.Balancers.ModifyRule(ctx, &elbv2.ModifyRuleInput{RuleArn: aws.String(rule), Actions: prior}); err != nil {
		return errors.Join(inactive, fmt.Errorf("put the rule %s back on what it forwarded to before: %w", rule, err))
	}
	return inactive
}

func (s *stack) deleteRule(ctx context.Context, c Clients, rule string) error {
	if rule == "" {
		return nil
	}
	_, err := c.Balancers.DeleteRule(ctx, &elbv2.DeleteRuleInput{RuleArn: aws.String(rule)})
	var gone *elbv2types.RuleNotFoundException
	if err != nil && !errors.As(err, &gone) {
		return fmt.Errorf("delete the rule %s: %w", rule, err)
	}
	return nil
}

func readTargetGroups(ctx context.Context, c Clients, tier environment.Tier, records map[string]router.DeploymentRecord) (map[string]string, error) {
	byService := map[string]string{}
	for _, app := range slices.Sorted(maps.Keys(records)) {
		physical := records[app].Physical
		if physical == "" {
			return nil, fmt.Errorf("the release of %s the promotion names runs no container the load balancer could forward to", app)
		}
		byService[physical] = app
	}
	groups := make(map[string]string, len(records))
	for batch := range slices.Chunk(slices.Sorted(maps.Keys(byService)), servicesPerRead) {
		read, err := c.Services.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(awsports.ContainerClusterName(tier)),
			Services: batch,
		})
		if err != nil {
			return nil, fmt.Errorf("read the services %v: %w", batch, err)
		}
		for _, service := range read.Services {
			for _, balanced := range service.LoadBalancers {
				if group := aws.ToString(balanced.TargetGroupArn); group != "" {
					groups[byService[aws.ToString(service.ServiceName)]] = group
				}
			}
		}
	}
	for _, app := range slices.Sorted(maps.Keys(records)) {
		if groups[app] == "" {
			return nil, fmt.Errorf("the cluster runs no service %s behind a load balancer, which the release of %s the promotion names", records[app].Physical, app)
		}
	}
	return groups, nil
}
