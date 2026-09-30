package provider

import "time"

type WorkerCeiling struct {
	Compute     Compute
	MaxDuration time.Duration
	Unbounded   bool
}
