package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestBoundedCaptureAnnotate(t *testing.T) {
	t.Run("passes the error through untouched when nothing was captured", func(t *testing.T) {
		c := &boundedCapture{}
		err := errors.New("exit status 1")
		got := c.annotate(err)
		if got != err {
			t.Errorf("annotate() = %v, want the original error unwrapped", got)
		}
	})

	t.Run("appends captured output to the error", func(t *testing.T) {
		c := &boundedCapture{}
		if _, err := c.Write([]byte("Error: read STRIPE_API_KEY (project root): unavailable\n")); err != nil {
			t.Fatalf("Write() = %v", err)
		}
		err := c.annotate(errors.New("exit status 1"))
		if !strings.Contains(err.Error(), "STRIPE_API_KEY (project root)") {
			t.Errorf("annotate() = %q, want it to carry the captured detail", err)
		}
		if !strings.Contains(err.Error(), "exit status 1") {
			t.Errorf("annotate() = %q, want the original error preserved", err)
		}
	})

	t.Run("caps how much it captures", func(t *testing.T) {
		c := &boundedCapture{}
		chunk := strings.Repeat("x", 1024)
		for i := 0; i < 8; i++ {
			if _, err := c.Write([]byte(chunk)); err != nil {
				t.Fatalf("Write() = %v", err)
			}
		}
		if got := c.buf.Len(); got != maxCapturedDiscoveryOutput {
			t.Errorf("captured %d bytes, want it capped at %d", got, maxCapturedDiscoveryOutput)
		}
	})

	t.Run("Write always reports the full length written, even once capped", func(t *testing.T) {
		c := &boundedCapture{}
		p := []byte(strings.Repeat("y", maxCapturedDiscoveryOutput+100))
		n, err := c.Write(p)
		if err != nil {
			t.Fatalf("Write() = %v", err)
		}
		if n != len(p) {
			t.Errorf("Write() n = %d, want %d so io.MultiWriter doesn't treat this as a short write", n, len(p))
		}
	})
}
