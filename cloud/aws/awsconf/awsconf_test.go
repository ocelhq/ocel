package awsconf

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
)

// Pins the deploy host's profile: adaptive, and above the SDK's default of 3.
func TestControlRetryerIsAdaptiveAndWiderThanTheDefault(t *testing.T) {
	r := ControlRetryer()
	if _, ok := r.(*retry.AdaptiveMode); !ok {
		t.Fatalf("ControlRetryer() = %T, want *retry.AdaptiveMode", r)
	}
	if got := r.MaxAttempts(); got != controlMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, controlMaxAttempts)
	}
}

// Pins the in-function profile: standard rather than adaptive, and above the
// SDK's default of 3.
func TestRuntimeRetryerIsStandardAndWiderThanTheDefault(t *testing.T) {
	r := runtimeRetryer()
	if _, ok := r.(*retry.Standard); !ok {
		t.Fatalf("runtimeRetryer() = %T, want *retry.Standard", r)
	}
	if got := r.MaxAttempts(); got != runtimeMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, runtimeMaxAttempts)
	}
}

// A loaded config has to carry the policy through to the clients built from it;
// setting the retryer on LoadOptions and having it dropped during resolution
// would leave every call site silently on the default.
func TestLoadedConfigsCarryTheirRetryer(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")

	control, err := Control(context.Background(), "eu-west-1")
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if control.Region != "eu-west-1" {
		t.Fatalf("Region = %q, want eu-west-1", control.Region)
	}
	if control.Retryer == nil {
		t.Fatal("Control config carries no retryer")
	}
	if got := control.Retryer().MaxAttempts(); got != controlMaxAttempts {
		t.Fatalf("control MaxAttempts() = %d, want %d", got, controlMaxAttempts)
	}

	runtime, err := Runtime(context.Background())
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	if runtime.Retryer == nil {
		t.Fatal("Runtime config carries no retryer")
	}
	if got := runtime.Retryer().MaxAttempts(); got != runtimeMaxAttempts {
		t.Fatalf("runtime MaxAttempts() = %d, want %d", got, runtimeMaxAttempts)
	}
}

// An empty region leaves the ambient chain to decide, rather than pinning the
// config to "".
func TestControlWithNoRegionFallsBackToTheAmbientChain(t *testing.T) {
	t.Setenv("AWS_REGION", "ap-southeast-2")

	cfg, err := Control(context.Background(), "")
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if cfg.Region != "ap-southeast-2" {
		t.Fatalf("Region = %q, want ap-southeast-2", cfg.Region)
	}
}
