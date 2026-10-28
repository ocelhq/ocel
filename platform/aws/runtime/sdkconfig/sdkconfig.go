package sdkconfig

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
)

const workloadMaxAttempts = 5

func Workload(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
	opts := append([]func(*config.LoadOptions) error{config.WithRetryer(func() aws.Retryer { return WorkloadRetryer() })}, optFns...)
	return config.LoadDefaultConfig(ctx, opts...)
}

func WorkloadRetryer() aws.RetryerV2 {
	return retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = workloadMaxAttempts
	})
}
