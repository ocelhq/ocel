package provider

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type WorkerCeiling struct {
	Compute     Compute
	MaxDuration time.Duration
	Unbounded   bool
}

func WorkerCeilingMessages(ceilings []WorkerCeiling) []*contractv1.WorkerCeiling {
	out := make([]*contractv1.WorkerCeiling, 0, len(ceilings))
	for _, ceiling := range ceilings {
		message := &contractv1.WorkerCeiling{Compute: string(ceiling.Compute)}
		if !ceiling.Unbounded {
			message.MaxDuration = durationpb.New(ceiling.MaxDuration)
		}
		out = append(out, message)
	}
	return out
}

func WorkerCeilingsOf(messages []*contractv1.WorkerCeiling) []WorkerCeiling {
	out := make([]WorkerCeiling, 0, len(messages))
	for _, message := range messages {
		ceiling := WorkerCeiling{Compute: Compute(message.GetCompute()), Unbounded: message.GetMaxDuration() == nil}
		if !ceiling.Unbounded {
			ceiling.MaxDuration = message.GetMaxDuration().AsDuration()
		}
		out = append(out, ceiling)
	}
	return out
}
