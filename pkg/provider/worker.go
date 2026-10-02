package provider

import "fmt"

const MaxConcurrency = 1000

type WorkerSpec struct {
	Name        string
	Concurrency int
}

func RefuseConcurrency(concurrency int32) error {
	if concurrency != 0 && (concurrency < 1 || concurrency > MaxConcurrency) {
		return fmt.Errorf("has concurrency %d, and concurrency is 1 to %d", concurrency, MaxConcurrency)
	}
	return nil
}
