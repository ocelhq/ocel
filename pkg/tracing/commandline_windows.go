package tracing

import (
	"os/exec"

	"go.opentelemetry.io/otel/attribute"
)

func commandLine(cmd *exec.Cmd) []attribute.KeyValue {
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine == "" {
		return nil
	}
	return []attribute.KeyValue{CommandLineKey.String(cmd.SysProcAttr.CmdLine)}
}
