package provider

import (
	"strings"
	"testing"
)

func TestAConcurrencyUnsetOrFrom1To1000IsAccepted(t *testing.T) {
	t.Parallel()

	for _, concurrency := range []int32{0, 1, 1000} {
		if err := RefuseConcurrency(concurrency); err != nil {
			t.Errorf("RefuseConcurrency(%d) = %v, want it accepted", concurrency, err)
		}
	}
}

func TestAConcurrencyBelow1OrAbove1000IsRefused(t *testing.T) {
	t.Parallel()

	for _, concurrency := range []int32{-1, 1001} {
		err := RefuseConcurrency(concurrency)
		if err == nil || !strings.Contains(err.Error(), "1 to 1000") {
			t.Errorf("RefuseConcurrency(%d) = %v, want it refused naming 1 to 1000", concurrency, err)
		}
	}
}
