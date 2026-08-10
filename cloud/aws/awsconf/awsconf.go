// Package awsconf is the one place a Go binary in this repo builds an AWS
// configuration, so no call site is left on the SDK's bare defaults — three
// attempts, no client-side pacing — against services that throttle per key
// rather than per account. (The TypeScript packages build their own
// `@aws-sdk/client-*` clients and are not covered by it.) SSM Parameter Store
// is the sharp edge: its write throughput is enforced per parameter, so
// concurrent deploys of one project contend on a single name no account-level
// quota increase can widen.
//
// The two profiles differ in what they are allowed to trade for a call that
// survives:
//
//   - Control is the deploy host, where a deploy that waits is strictly better
//     than a deploy that fails. Its wider attempt ceiling is what carries a
//     call through contention from deploys in other processes, which nothing
//     client-side can coordinate. Adaptive mode adds what a single process can
//     do for itself: its rate limiter is per process, so it learns from the
//     throttles this deploy sees and paces this deploy's later calls across all
//     the clients built from the config — a long deploy stops hammering a key
//     it has already been throttled on, which per-call backoff never learns.
//   - Runtime is code inside a deployed function, where that same limiter would
//     charge an unrelated throttle to a cold start. It gets a wider standard
//     backoff and no pacing.
package awsconf

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
)

// The attempt ceilings each profile raises the SDK's default of 3 to. Both are
// ceilings, not schedules: a call that is not throttled still returns on its
// first attempt.
const (
	controlMaxAttempts = 8
	runtimeMaxAttempts = 5
)

// Control loads the deploy host's configuration, in region when one is named
// and from the ambient chain otherwise.
func Control(ctx context.Context, region string) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRetryer(ControlRetryer)}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	return config.LoadDefaultConfig(ctx, opts...)
}

// ControlRetryer is the deploy host's retry policy, exported for the clients
// built from a hand-assembled aws.Config (one addressing a third-party
// S3-compatible store with its own credential) rather than from Control.
func ControlRetryer() aws.Retryer {
	return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
		o.StandardOptions = append(o.StandardOptions, func(s *retry.StandardOptions) {
			s.MaxAttempts = controlMaxAttempts
		})
	})
}

// Runtime loads the configuration for code running inside a deployed function.
// The caller's options are applied after the retry policy so a caller may still
// override it.
func Runtime(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
	opts := append([]func(*config.LoadOptions) error{config.WithRetryer(runtimeRetryer)}, optFns...)
	return config.LoadDefaultConfig(ctx, opts...)
}

// runtimeRetryer is the in-function retry policy: a wider backoff than the
// SDK's default, without adaptive mode's client-side pacing.
func runtimeRetryer() aws.Retryer {
	return retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = runtimeMaxAttempts
	})
}
