package sdkconfig

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go/middleware"
)

const controlMaxAttempts = 8

func Control(ctx context.Context, region string) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRetryer(ControlRetryer),
		config.WithAPIOptions([]func(*middleware.Stack) error{addOperationSpan}),
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, append(opts, config.WithHTTPClient(NewTracedClient(nil)))...)
	if refusesTracedClient(err) {
		// HACK: the SDK loads a custom CA bundle only into its own client, so with one the calls that load credentials go untraced.
		cfg, err = config.LoadDefaultConfig(ctx, opts...)
	}
	if err != nil {
		return aws.Config{}, err
	}
	cfg.HTTPClient = NewTracedClient(cfg.HTTPClient)
	return cfg, nil
}

const customCABundleRefusal = "unable to add custom RootCAs HTTPClient"

func refusesTracedClient(err error) bool {
	return err != nil && strings.Contains(err.Error(), customCABundleRefusal)
}

func ControlRetryer() aws.Retryer {
	return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
		o.StandardOptions = append(o.StandardOptions, func(s *retry.StandardOptions) {
			s.MaxAttempts = controlMaxAttempts
		})
	})
}
