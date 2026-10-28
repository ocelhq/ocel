//go:build !windows

package tracing

import (
	"os/exec"

	"go.opentelemetry.io/otel/attribute"
)

func commandLine(*exec.Cmd) []attribute.KeyValue {
	return nil
}
