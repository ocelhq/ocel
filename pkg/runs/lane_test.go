package runs_test

import (
	"testing"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

func TestAMessageRunsInTheLaneItAsksForWhenItsConsumerReadsThatLane(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		reads []provider.Lane
		asked topicv1.Lane
		want  string
	}{
		{"no lane asked", nil, topicv1.Lane_LANE_UNSPECIFIED, "default"},
		{"a consumer reading every lane", nil, topicv1.Lane_LANE_HIGH, "high"},
		{"a lane the consumer reads", []provider.Lane{provider.LaneLow, provider.LaneHigh}, topicv1.Lane_LANE_LOW, "low"},
		{"a lane the consumer does not read", []provider.Lane{provider.LaneHigh}, topicv1.Lane_LANE_LOW, "default"},
	} {
		if got := runs.LaneFor(tc.reads, tc.asked); got != tc.want {
			t.Errorf("%s: LaneFor(%v, %v) = %q, want %q", tc.name, tc.reads, tc.asked, got, tc.want)
		}
	}
}
