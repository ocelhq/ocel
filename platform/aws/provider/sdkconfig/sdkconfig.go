package sdkconfig

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
)

const (
	controlMaxAttempts = 8
	runtimeMaxAttempts = 5
)

func Control(ctx context.Context, region string) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRetryer(ControlRetryer)}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	return config.LoadDefaultConfig(ctx, opts...)
}

func ControlRetryer() aws.Retryer {
	return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
		o.StandardOptions = append(o.StandardOptions, func(s *retry.StandardOptions) {
			s.MaxAttempts = controlMaxAttempts
		})
	})
}

func Runtime(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
	opts := append([]func(*config.LoadOptions) error{config.WithRetryer(runtimeRetryer)}, optFns...)
	return config.LoadDefaultConfig(ctx, opts...)
}

func runtimeRetryer() aws.Retryer {
	return retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = runtimeMaxAttempts
	})
}
