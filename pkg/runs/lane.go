package runs

import (
	"slices"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var lanes = map[topicv1.Lane]provider.Lane{
	topicv1.Lane_LANE_HIGH:    provider.LaneHigh,
	topicv1.Lane_LANE_DEFAULT: provider.LaneDefault,
	topicv1.Lane_LANE_LOW:     provider.LaneLow,
}

func LaneOf(lane topicv1.Lane) provider.Lane {
	if named, found := lanes[lane]; found {
		return named
	}
	return provider.LaneDefault
}

func LaneFor(reads []provider.Lane, asked provider.Lane) provider.Lane {
	if asked == "" || (len(reads) > 0 && !slices.Contains(reads, asked)) {
		return provider.LaneDefault
	}
	return asked
}
