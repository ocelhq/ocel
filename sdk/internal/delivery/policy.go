package delivery

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
)

func EncodeRetry(maxAttempts int, minDelay, maxDelay time.Duration) *resourcesv1.RetryPolicy {
	return &resourcesv1.RetryPolicy{
		MaxAttempts: int32(maxAttempts),
		MinDelay:    newOptionalDuration(minDelay),
		MaxDelay:    newOptionalDuration(maxDelay),
	}
}

func EnsureBatchPolicy(policy **resourcesv1.BatchPolicy) *resourcesv1.BatchPolicy {
	if *policy == nil {
		*policy = &resourcesv1.BatchPolicy{}
	}
	return *policy
}

func EncodeDueAt(due func(now time.Time) time.Time) *timestamppb.Timestamp {
	if due == nil {
		return nil
	}
	return timestamppb.New(due(time.Now()))
}

func newOptionalDuration(duration time.Duration) *durationpb.Duration {
	if duration == 0 {
		return nil
	}
	return durationpb.New(duration)
}
