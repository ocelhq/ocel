package sdkconfig

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
)

func TestWorkloadRetryerIsStandardAndWiderThanTheDefault(t *testing.T) {
	r := WorkloadRetryer()
	if _, ok := r.(*retry.Standard); !ok {
		t.Fatalf("WorkloadRetryer() = %T, want *retry.Standard", r)
	}
	if got := r.MaxAttempts(); got != workloadMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, workloadMaxAttempts)
	}
}

func TestWorkloadConfigIncludesItsRetryer(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")

	cfg, err := Workload(context.Background())
	if err != nil {
		t.Fatalf("Workload: %v", err)
	}
	if cfg.Retryer == nil {
		t.Fatal("Workload config has no retryer")
	}
	if got := cfg.Retryer().MaxAttempts(); got != workloadMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, workloadMaxAttempts)
	}
}
