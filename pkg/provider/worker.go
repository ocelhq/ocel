package provider

import (
	"fmt"
	"slices"
)

const MaxConcurrency = 1000

type WorkerSpec struct {
	Name        string
	Concurrency int
	App         string
	Compute     Compute
}

func IsWorkerApp(app string, workers []WorkerSpec) bool {
	return slices.ContainsFunc(workers, func(worker WorkerSpec) bool { return worker.Name == app && worker.App == app })
}

func RefuseConcurrency(concurrency int32) error {
	if concurrency != 0 && (concurrency < 1 || concurrency > MaxConcurrency) {
		return fmt.Errorf("has concurrency %d, and concurrency is 1 to %d", concurrency, MaxConcurrency)
	}
	return nil
}
