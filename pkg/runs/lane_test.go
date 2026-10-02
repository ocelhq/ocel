package runs_test

import (
	"testing"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

func TestEachWireLaneIsTheProviderLaneOfItsNameAndAnUnnamedOneIsTheDefault(t *testing.T) {
	t.Parallel()

	for wire, want := range map[topicv1.Lane]provider.Lane{
		topicv1.Lane_LANE_HIGH:        provider.LaneHigh,
		topicv1.Lane_LANE_DEFAULT:     provider.LaneDefault,
		topicv1.Lane_LANE_LOW:         provider.LaneLow,
		topicv1.Lane_LANE_UNSPECIFIED: provider.LaneDefault,
	} {
		if got := runs.LaneOf(wire); got != want {
			t.Errorf("LaneOf(%v) = %q, want %q", wire, got, want)
		}
	}
}

func TestAMessageRunsInTheLaneItAsksForWhenItsConsumerReadsThatLane(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		reads []provider.Lane
		asked topicv1.Lane
		want  provider.Lane
	}{
		{"no lane asked", nil, topicv1.Lane_LANE_UNSPECIFIED, provider.LaneDefault},
		{"a consumer reading every lane", nil, topicv1.Lane_LANE_HIGH, provider.LaneHigh},
		{"a lane the consumer reads", []provider.Lane{provider.LaneLow, provider.LaneHigh}, topicv1.Lane_LANE_LOW, provider.LaneLow},
		{"a lane the consumer does not read", []provider.Lane{provider.LaneHigh}, topicv1.Lane_LANE_LOW, provider.LaneDefault},
	} {
		if got := runs.LaneFor(tc.reads, tc.asked); got != tc.want {
			t.Errorf("%s: LaneFor(%v, %v) = %q, want %q", tc.name, tc.reads, tc.asked, got, tc.want)
		}
	}
}
