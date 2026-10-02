package provider

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestAConsumerDeclaringNoRetryRunsTheDefaults(t *testing.T) {
	t.Parallel()

	want := RetryPolicy{MaxAttempts: 3, MinDelay: time.Second, MaxDelay: time.Minute}
	if got := ResolveRetryPolicy(nil, nil); got != want {
		t.Errorf("ResolveRetryPolicy(nil, nil) = %+v, want %+v", got, want)
	}
}

func TestAConsumersRetryOverridesItsTopicsFieldByField(t *testing.T) {
	t.Parallel()

	topic := &resourcesv1.RetryPolicy{MaxAttempts: 7, MinDelay: durationpb.New(2 * time.Second), MaxDelay: durationpb.New(5 * time.Minute)}
	consumer := &resourcesv1.RetryPolicy{MaxAttempts: 4, MaxDelay: durationpb.New(30 * time.Second)}
	want := RetryPolicy{MaxAttempts: 4, MinDelay: 2 * time.Second, MaxDelay: 30 * time.Second}
	if got := ResolveRetryPolicy(topic, consumer); got != want {
		t.Errorf("ResolveRetryPolicy(topic, consumer) = %+v, want %+v", got, want)
	}
}

func TestAMinDelayAboveTheMaxDelayRaisesTheMaxDelayToIt(t *testing.T) {
	t.Parallel()

	want := RetryPolicy{MaxAttempts: 3, MinDelay: 5 * time.Minute, MaxDelay: 5 * time.Minute}
	if got := ResolveRetryPolicy(&resourcesv1.RetryPolicy{MinDelay: durationpb.New(5 * time.Minute)}); got != want {
		t.Errorf("ResolveRetryPolicy(minDelay 5m) = %+v, want %+v", got, want)
	}
}

func TestARetryWithinOcelsLimitsIsAccepted(t *testing.T) {
	t.Parallel()

	for name, retry := range map[string]*resourcesv1.RetryPolicy{
		"none":         nil,
		"empty":        {},
		"every bound":  {MaxAttempts: 100, MinDelay: durationpb.New(10 * time.Minute), MaxDelay: durationpb.New(10 * time.Minute)},
		"no time":      {MaxAttempts: 1, MinDelay: durationpb.New(0), MaxDelay: durationpb.New(0)},
		"attempts set": {MaxAttempts: 5},
	} {
		if err := RefuseRetry(retry); err != nil {
			t.Errorf("RefuseRetry(%s) = %v, want it accepted", name, err)
		}
	}
}

func TestARetryBeyondOcelsLimitsIsRefused(t *testing.T) {
	t.Parallel()

	seconds := func(n int64) *durationpb.Duration { return durationpb.New(time.Duration(n) * time.Second) }
	for name, tc := range map[string]struct {
		retry *resourcesv1.RetryPolicy
		says  []string
	}{
		"attempts above 100":     {&resourcesv1.RetryPolicy{MaxAttempts: 101}, []string{"maxAttempts 101", "1 to 100"}},
		"negative attempts":      {&resourcesv1.RetryPolicy{MaxAttempts: -1}, []string{"maxAttempts -1"}},
		"a negative min delay":   {&resourcesv1.RetryPolicy{MinDelay: seconds(-1)}, []string{"minDelay -1s", "never negative"}},
		"a min delay above 600s": {&resourcesv1.RetryPolicy{MinDelay: seconds(601)}, []string{"minDelay 10m1s", "at most 10m0s"}},
		"a negative max delay":   {&resourcesv1.RetryPolicy{MaxDelay: seconds(-1)}, []string{"maxDelay -1s"}},
		"a max delay above 600s": {&resourcesv1.RetryPolicy{MaxDelay: seconds(601)}, []string{"maxDelay 10m1s", "at most 10m0s"}},
		"a min above its max":    {&resourcesv1.RetryPolicy{MinDelay: seconds(60), MaxDelay: seconds(30)}, []string{"minDelay 1m0s above its maxDelay 30s"}},
	} {
		err := RefuseRetry(tc.retry)
		if err == nil {
			t.Errorf("RefuseRetry(%s) = nil, want it refused", name)
			continue
		}
		for _, said := range tc.says {
			if !strings.Contains(err.Error(), said) {
				t.Errorf("RefuseRetry(%s) = %q, want it to say %q", name, err, said)
			}
		}
	}
}
