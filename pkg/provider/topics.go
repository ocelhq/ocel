package provider

import "time"

type TopicSpec struct {
	Schema    string
	Ordered   bool
	TTL       time.Duration
	Cron      string
	Consumers []ConsumerSpec
}

type ConsumerSpec struct {
	Name        string
	Worker      string
	Exclusive   bool
	Retry       RetryPolicy
	Concurrency int
	MaxDuration time.Duration
	Lanes       []Lane
	Batch       *BatchPolicy
}

type Lane string

const (
	LaneHigh    Lane = "high"
	LaneDefault Lane = "default"
	LaneLow     Lane = "low"
)

type BatchPolicy struct {
	Size    int
	Timeout time.Duration
}
