package provider

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const (
	DefaultRetryMaxAttempts = 3
	DefaultRetryMinDelay    = time.Second
	DefaultRetryMaxDelay    = time.Minute

	MaxRetryAttempts = 100
	MaxRetryDelay    = 600 * time.Second
)

type RetryPolicy struct {
	MaxAttempts int
	MinDelay    time.Duration
	MaxDelay    time.Duration
}

func ResolveRetryPolicy(declared ...*resourcesv1.RetryPolicy) RetryPolicy {
	policy := RetryPolicy{MaxAttempts: DefaultRetryMaxAttempts, MinDelay: DefaultRetryMinDelay, MaxDelay: DefaultRetryMaxDelay}
	for _, retry := range declared {
		if retry.GetMaxAttempts() > 0 {
			policy.MaxAttempts = int(retry.GetMaxAttempts())
		}
		if retry.GetMinDelay() != nil {
			policy.MinDelay = retry.GetMinDelay().AsDuration()
		}
		if retry.GetMaxDelay() != nil {
			policy.MaxDelay = retry.GetMaxDelay().AsDuration()
		}
	}
	policy.MaxDelay = max(policy.MaxDelay, policy.MinDelay)
	return policy
}

func RefuseRetry(retry *resourcesv1.RetryPolicy) error {
	if retry == nil {
		return nil
	}
	if attempts := retry.GetMaxAttempts(); attempts != 0 && (attempts < 1 || attempts > MaxRetryAttempts) {
		return fmt.Errorf("retries with maxAttempts %d, and maxAttempts is 1 to %d", attempts, MaxRetryAttempts)
	}
	minDelay, maxDelay := retry.GetMinDelay(), retry.GetMaxDelay()
	if err := refuseRetryDelay("minDelay", minDelay); err != nil {
		return err
	}
	if err := refuseRetryDelay("maxDelay", maxDelay); err != nil {
		return err
	}
	if minDelay != nil && maxDelay != nil && minDelay.AsDuration() > maxDelay.AsDuration() {
		return fmt.Errorf("retries with minDelay %v above its maxDelay %v", minDelay.AsDuration(), maxDelay.AsDuration())
	}
	return nil
}

func refuseRetryDelay(field string, delay *durationpb.Duration) error {
	if delay == nil {
		return nil
	}
	switch d := delay.AsDuration(); {
	case d < 0:
		return fmt.Errorf("retries with %s %v, and a delay is never negative", field, d)
	case d > MaxRetryDelay:
		return fmt.Errorf("retries with %s %v, and a delay is at most %v", field, d, MaxRetryDelay)
	}
	return nil
}
