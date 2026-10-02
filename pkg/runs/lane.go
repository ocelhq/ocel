package runs

import (
	"slices"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func LaneName(lane topicv1.Lane) string {
	switch lane {
	case topicv1.Lane_LANE_HIGH:
		return string(provider.LaneHigh)
	case topicv1.Lane_LANE_LOW:
		return string(provider.LaneLow)
	default:
		return string(provider.LaneDefault)
	}
}

func LaneFor(reads []provider.Lane, lane topicv1.Lane) string {
	asked := provider.Lane(LaneName(lane))
	if lane == topicv1.Lane_LANE_UNSPECIFIED || (len(reads) > 0 && !slices.Contains(reads, asked)) {
		return string(provider.LaneDefault)
	}
	return string(asked)
}
