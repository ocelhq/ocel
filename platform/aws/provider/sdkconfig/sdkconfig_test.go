package sdkconfig

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
)

func TestRetryer(t *testing.T) {
	t.Run("control is adaptive and wider than the default", func(t *testing.T) {
		r := ControlRetryer()
		if _, ok := r.(*retry.AdaptiveMode); !ok {
			t.Fatalf("ControlRetryer() = %T, want *retry.AdaptiveMode", r)
		}
		if got := r.MaxAttempts(); got != controlMaxAttempts {
			t.Fatalf("MaxAttempts() = %d, want %d", got, controlMaxAttempts)
		}
	})

	t.Run("runtime is standard and wider than the default", func(t *testing.T) {
		r := runtimeRetryer()
		if _, ok := r.(*retry.Standard); !ok {
			t.Fatalf("runtimeRetryer() = %T, want *retry.Standard", r)
		}
		if got := r.MaxAttempts(); got != runtimeMaxAttempts {
			t.Fatalf("MaxAttempts() = %d, want %d", got, runtimeMaxAttempts)
		}
	})
}

func TestLoad(t *testing.T) {
	t.Run("configs carry their retryer", func(t *testing.T) {
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
	})

	t.Run("control with no region falls back to the ambient chain", func(t *testing.T) {
		t.Setenv("AWS_REGION", "ap-southeast-2")

		cfg, err := Control(context.Background(), "")
		if err != nil {
			t.Fatalf("Control: %v", err)
		}
		if cfg.Region != "ap-southeast-2" {
			t.Fatalf("Region = %q, want ap-southeast-2", cfg.Region)
		}
	})
}
