package delivery

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func EncodeDueAt(due func(now time.Time) time.Time) *timestamppb.Timestamp {
	if due == nil {
		return nil
	}
	return timestamppb.New(due(time.Now()))
}
