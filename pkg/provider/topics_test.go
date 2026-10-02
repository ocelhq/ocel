package provider

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

func refusedSaying(t *testing.T, call string, err error, says ...string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s = nil, want it refused", call)
		return
	}
	for _, said := range says {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("%s = %q, want it to say %q", call, err, said)
		}
	}
}

func TestATTLUnsetOrUpTo14DaysIsAccepted(t *testing.T) {
	t.Parallel()

	for _, ttl := range []*durationpb.Duration{nil, durationpb.New(time.Second), durationpb.New(14 * 24 * time.Hour)} {
		if err := RefuseTTL(ttl); err != nil {
			t.Errorf("RefuseTTL(%v) = %v, want it accepted", ttl.AsDuration(), err)
		}
	}
}

func TestATTLOfNoTimeOrAbove14DaysIsRefused(t *testing.T) {
	t.Parallel()

	refusedSaying(t, "RefuseTTL(0)", RefuseTTL(durationpb.New(0)), "ttl 0s")
	refusedSaying(t, "RefuseTTL(-1s)", RefuseTTL(durationpb.New(-time.Second)), "ttl -1s")
	refusedSaying(t, "RefuseTTL(15 days)", RefuseTTL(durationpb.New(15*24*time.Hour)), "ttl 360h0m0s", "336h0m0s")
}

func TestAMaxDurationOfNoTimeIsRefused(t *testing.T) {
	t.Parallel()

	if err := RefuseMaxDuration(nil); err != nil {
		t.Errorf("RefuseMaxDuration(nil) = %v, want it accepted", err)
	}
	if err := RefuseMaxDuration(durationpb.New(time.Minute)); err != nil {
		t.Errorf("RefuseMaxDuration(1m) = %v, want it accepted", err)
	}
	refusedSaying(t, "RefuseMaxDuration(0)", RefuseMaxDuration(durationpb.New(0)), "maxDuration 0s")
	refusedSaying(t, "RefuseMaxDuration(-1s)", RefuseMaxDuration(durationpb.New(-time.Second)), "maxDuration -1s")
}

func TestABatchWithinOcelsLimitsIsAccepted(t *testing.T) {
	t.Parallel()

	for _, batch := range []*resourcesv1.BatchPolicy{nil, {Size: 1}, {Size: 1000, Timeout: durationpb.New(5 * time.Minute)}} {
		if err := RefuseBatch(batch); err != nil {
			t.Errorf("RefuseBatch(%v) = %v, want it accepted", batch, err)
		}
	}
}

func TestABatchBeyondOcelsLimitsIsRefused(t *testing.T) {
	t.Parallel()

	refusedSaying(t, "RefuseBatch(size 0)", RefuseBatch(&resourcesv1.BatchPolicy{}), "batch size 0", "1 to 1000")
	refusedSaying(t, "RefuseBatch(size 1001)", RefuseBatch(&resourcesv1.BatchPolicy{Size: 1001}), "batch size 1001")
	refusedSaying(t, "RefuseBatch(timeout -1s)", RefuseBatch(&resourcesv1.BatchPolicy{Size: 1, Timeout: durationpb.New(-time.Second)}), "batch timeout -1s")
	refusedSaying(t, "RefuseBatch(timeout 301s)", RefuseBatch(&resourcesv1.BatchPolicy{Size: 1, Timeout: durationpb.New(301 * time.Second)}), "batch timeout 5m1s", "5m0s")
}

func TestAConsumerOfAnOrderedTopicBatchingAtMost10WithNoLanesIsAccepted(t *testing.T) {
	t.Parallel()

	if err := RefuseOrderedConsumer(nil, &resourcesv1.BatchPolicy{Size: 10}); err != nil {
		t.Errorf("RefuseOrderedConsumer(batch 10) = %v, want it accepted", err)
	}
}

func TestAConsumerOfAnOrderedTopicBatchingAbove10OrReadingLanesIsRefused(t *testing.T) {
	t.Parallel()

	refusedSaying(t, "RefuseOrderedConsumer(batch 11)", RefuseOrderedConsumer(nil, &resourcesv1.BatchPolicy{Size: 11}), "batch size 11", "ordered", "at most 10")
	refusedSaying(t, "RefuseOrderedConsumer(lanes)", RefuseOrderedConsumer([]topicv1.Lane{topicv1.Lane_LANE_HIGH}, nil), "lanes", "ordered")
}

func TestAnOrderedTaskThatBatchesIsRefused(t *testing.T) {
	t.Parallel()

	if err := RefuseOrderedTask(true, nil); err != nil {
		t.Errorf("RefuseOrderedTask(ordered, no batch) = %v, want it accepted", err)
	}
	if err := RefuseOrderedTask(false, &resourcesv1.BatchPolicy{Size: 5}); err != nil {
		t.Errorf("RefuseOrderedTask(unordered, batch) = %v, want it accepted", err)
	}
	refusedSaying(t, "RefuseOrderedTask(ordered, batch)", RefuseOrderedTask(true, &resourcesv1.BatchPolicy{Size: 5}), "ordered and batches")
}

func TestATopicTakesAtMost1000ConsumersOr100WhenOrdered(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		count   int
		ordered bool
	}{{1000, false}, {100, true}} {
		if err := RefuseConsumerCount(tc.count, tc.ordered); err != nil {
			t.Errorf("RefuseConsumerCount(%d, ordered %v) = %v, want it accepted", tc.count, tc.ordered, err)
		}
	}
	refusedSaying(t, "RefuseConsumerCount(1001)", RefuseConsumerCount(1001, false), "1001 consumers", "at most 1000")
	refusedSaying(t, "RefuseConsumerCount(101, ordered)", RefuseConsumerCount(101, true), "101 consumers", "at most 100", "ordered")
}
