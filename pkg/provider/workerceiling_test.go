package provider

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestAWorkerCeilingReadOffTheWireKeepsItsBoundOrItsAbsence(t *testing.T) {
	t.Parallel()

	got := WorkerCeilingsOf([]*contractv1.WorkerCeiling{
		{Compute: "serverless", MaxDuration: durationpb.New(15 * time.Minute)},
		{Compute: "container"},
	})
	want := []WorkerCeiling{
		{Compute: ComputeServerless, MaxDuration: 15 * time.Minute},
		{Compute: ComputeContainer, Unbounded: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("WorkerCeilingsOf() = %+v, want %+v", got, want)
	}
}
