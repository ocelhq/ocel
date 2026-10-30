package queues

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAWorkerOnContainerComputeRunsAtMostTwelveHoursTheLongestSQSHoldsAMessage(t *testing.T) {
	t.Parallel()

	ceiling := workerCeilingOn(t, provider.ComputeContainer)
	if ceiling.Unbounded || ceiling.MaxDuration != 12*time.Hour {
		t.Errorf("container worker ceiling = %+v, want 12h: SQS extends a message's visibility to 12 hours and no further", ceiling)
	}
	if serverless := workerCeilingOn(t, provider.ComputeServerless); serverless.MaxDuration != 15*time.Minute {
		t.Errorf("serverless worker ceiling = %v, want 15m, Lambda's own", serverless.MaxDuration)
	}
}

func workerCeilingOn(t *testing.T, compute provider.Compute) provider.WorkerCeiling {
	t.Helper()
	for _, ceiling := range WorkerCeilings {
		if ceiling.Compute == compute {
			return ceiling
		}
	}
	t.Fatalf("WorkerCeilings names no ceiling for %s compute", compute)
	return provider.WorkerCeiling{}
}
