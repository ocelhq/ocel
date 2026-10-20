package container

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func rulePriority(physical string, taken map[int]bool) int {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(physical))
	start := int(sum.Sum32() % uint32(maxRulePriority))
	for step := range maxRulePriority {
		priority := 1 + (start+step)%maxRulePriority
		if !taken[priority] {
			return priority
		}
	}
	return 0
}

func takenPriorities(ctx context.Context, rules RulesAPI, listener, physical string) (map[int]bool, error) {
	taken := map[int]bool{}
	if rules == nil {
		return taken, nil
	}
	var marker *string
	for {
		page, err := rules.DescribeRules(ctx, &elbv2.DescribeRulesInput{ListenerArn: aws.String(listener), Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("read the rules already on the container front: %w", err)
		}
		for _, rule := range page.Rules {
			if routesContainer(rule, physical) {
				continue
			}
			if priority, err := strconv.Atoi(aws.ToString(rule.Priority)); err == nil {
				taken[priority] = true
			}
		}
		if marker = page.NextMarker; marker == nil {
			return taken, nil
		}
	}
}

func routesContainer(rule elbv2types.Rule, physical string) bool {
	for _, condition := range rule.Conditions {
		header := condition.HttpHeaderConfig
		if header == nil || !strings.EqualFold(aws.ToString(header.HttpHeaderName), edge.OriginContainerHeader) {
			continue
		}
		if slices.Contains(header.Values, physical) {
			return true
		}
	}
	return false
}

func (d *Definition) PlaceRule(ctx context.Context, rules RulesAPI) error {
	if d.workerApp {
		return nil
	}
	names, err := d.names()
	if err != nil {
		return err
	}
	taken, err := takenPriorities(ctx, rules, d.readListenerArn(), names.Service)
	if err != nil {
		return err
	}
	if d.priority = rulePriority(names.Service, taken); d.priority == 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"the container front for the %s tier has a rule at every priority a listener allows, so %s has no slot: prune the releases behind it", d.tier, d.App)
	}
	return nil
}

const (
	priorityInUseCode = "PriorityInUse"
	RulePlacements    = 3
)

func IsPriorityTaken(err error) bool {
	return err != nil && strings.Contains(err.Error(), priorityInUseCode)
}
