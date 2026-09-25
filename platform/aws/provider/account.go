package provider

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type classEdge struct {
	class providerkit.Class
	kind  edge.Kind
}

func (p *Provider) Region() string { return p.aws.Region }

func (p *Provider) bootstrapped(ctx context.Context, class providerkit.Class) (bootstrap.Deployed, error) {
	return p.deployed.resolve(class, func() (bootstrap.Deployed, error) {
		return bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(p.aws), p.namespace, string(class))
	})
}

func (p *Provider) classParams(ctx context.Context, class providerkit.Class, kind edge.Kind) (bootstrap.ClassParams, error) {
	return p.params.resolve(classEdge{class: class, kind: kind}, func() (bootstrap.ClassParams, error) {
		if kind == "" {
			return bootstrap.ReadCoreParams(ctx, ssm.NewFromConfig(p.aws), p.namespace, string(class))
		}
		return bootstrap.ReadClassParams(ctx, ssm.NewFromConfig(p.aws), p.namespace, string(class), kind)
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
	mu   sync.Mutex
	held map[K]V
}

func (m *memo[K, V]) resolve(key K, fill func() (V, error)) (V, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held, filled := m.held[key]; filled {
		return held, nil
	}
	value, err := fill()
	if err != nil {
		var zero V
		return zero, err
	}
	if m.held == nil {
		m.held = map[K]V{}
	}
	m.held[key] = value
	return value, nil
}

func (m *memo[K, V]) forget() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = nil
}
