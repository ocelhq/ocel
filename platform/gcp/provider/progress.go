package gcp

import "github.com/ocelhq/ocel/pkg/progress"

func ensureProgress(runProgress progress.Log) progress.Log {
	if runProgress == nil {
		return progress.Discard()
	}
	return runProgress
}
