package alb

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Balancers interface {
	DescribeLoadBalancers(ctx context.Context, in *elbv2.DescribeLoadBalancersInput, opts ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error)
	DescribeListeners(ctx context.Context, in *elbv2.DescribeListenersInput, opts ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error)
	ModifyListener(ctx context.Context, in *elbv2.ModifyListenerInput, opts ...func(*elbv2.Options)) (*elbv2.ModifyListenerOutput, error)
	DescribeRules(ctx context.Context, in *elbv2.DescribeRulesInput, opts ...func(*elbv2.Options)) (*elbv2.DescribeRulesOutput, error)
	CreateRule(ctx context.Context, in *elbv2.CreateRuleInput, opts ...func(*elbv2.Options)) (*elbv2.CreateRuleOutput, error)
	ModifyRule(ctx context.Context, in *elbv2.ModifyRuleInput, opts ...func(*elbv2.Options)) (*elbv2.ModifyRuleOutput, error)
	DeleteRule(ctx context.Context, in *elbv2.DeleteRuleInput, opts ...func(*elbv2.Options)) (*elbv2.DeleteRuleOutput, error)
	AddListenerCertificates(ctx context.Context, in *elbv2.AddListenerCertificatesInput, opts ...func(*elbv2.Options)) (*elbv2.AddListenerCertificatesOutput, error)
	RemoveListenerCertificates(ctx context.Context, in *elbv2.RemoveListenerCertificatesInput, opts ...func(*elbv2.Options)) (*elbv2.RemoveListenerCertificatesOutput, error)
	DescribeTrustStores(ctx context.Context, in *elbv2.DescribeTrustStoresInput, opts ...func(*elbv2.Options)) (*elbv2.DescribeTrustStoresOutput, error)
	CreateTrustStore(ctx context.Context, in *elbv2.CreateTrustStoreInput, opts ...func(*elbv2.Options)) (*elbv2.CreateTrustStoreOutput, error)
	ModifyTrustStore(ctx context.Context, in *elbv2.ModifyTrustStoreInput, opts ...func(*elbv2.Options)) (*elbv2.ModifyTrustStoreOutput, error)
}

type Services interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
}

type Objects interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type Clients struct {
	Balancers Balancers
	Services  Services
	Objects   Objects
	Bucket    string
}

func FromConfig(
	load func(context.Context) (aws.Config, error),
	artifactBucket func(context.Context, environment.Tier) (string, error),
) func(context.Context, environment.Tier) (Clients, error) {
	return func(ctx context.Context, tier environment.Tier) (Clients, error) {
		cfg, err := load(ctx)
		if err != nil {
			return Clients{}, err
		}
		bucket, err := artifactBucket(ctx, tier)
		if err != nil {
			return Clients{}, fmt.Errorf("read the bucket the %s tier keeps the trusted client certificates in: %w", tier, err)
		}
		return Clients{
			Balancers: elbv2.NewFromConfig(cfg),
			Services:  ecs.NewFromConfig(cfg),
			Objects:   s3.NewFromConfig(cfg),
			Bucket:    bucket,
		}, nil
	}
}
