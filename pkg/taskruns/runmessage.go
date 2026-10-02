package taskruns

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func NewRunMessage(run provider.Run) *taskv1.Run {
	return &taskv1.Run{
		Id:         run.Execution,
		Task:       run.Topic,
		Status:     wireStatuses[run.Status],
		Payload:    run.Payload,
		Output:     run.Output,
		Metadata:   run.Metadata,
		Error:      run.Error,
		Attempts:   int32(run.Attempts),
		Tags:       run.Tags,
		CreatedAt:  TimestampOf(run.CreatedAt),
		DueAt:      TimestampOf(run.DueAt),
		StartedAt:  TimestampOf(run.StartedAt),
		FinishedAt: TimestampOf(run.FinishedAt),
		ExpiresAt:  TimestampOf(run.ExpiresAt),
	}
}

func TimestampOf(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
