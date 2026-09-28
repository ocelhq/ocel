package aws

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type tierEdge struct {
	tier environment.Tier
	kind edge.Kind
}

func (p *Provider) Region() string { return p.aws.Region }

func (p *Provider) bootstrapped(ctx context.Context, tier environment.Tier) (bootstrap.Deployed, error) {
	return p.deployed.resolve(tier, func() (bootstrap.Deployed, error) {
		return bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(p.aws), p.namespace, string(tier))
	})
}

func (p *Provider) tierParams(ctx context.Context, tier environment.Tier, kind edge.Kind) (bootstrap.TierParams, error) {
	return p.params.resolve(tierEdge{tier: tier, kind: kind}, func() (bootstrap.TierParams, error) {
		if kind == "" {
			return bootstrap.ReadCoreParams(ctx, ssm.NewFromConfig(p.aws), p.namespace, string(tier))
		}
		return bootstrap.ReadTierParams(ctx, ssm.NewFromConfig(p.aws), p.namespace, string(tier), kind)
	})
}

func (p *Provider) accountID(ctx context.Context) (string, error) {
	return p.account.resolve(struct{}{}, func() (string, error) {
		out, err := sts.NewFromConfig(p.aws).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			return "", fmt.Errorf("resolve AWS account id: %w", err)
		}
		return aws.ToString(out.Account), nil
	})
}

type memo[K comparable, V any] struct {
	mu     sync.Mutex
	values map[K]V
}

func (m *memo[K, V]) resolve(key K, fill func() (V, error)) (V, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, filled := m.values[key]; filled {
		return cached, nil
	}
	value, err := fill()
	if err != nil {
		var zero V
		return zero, err
	}
	if m.values == nil {
		m.values = map[K]V{}
	}
	m.values[key] = value
	return value, nil
}

func (m *memo[K, V]) forget() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values = nil
}
