package gcloud

import "github.com/ocelhq/ocel/pkg/progress"

func EnsureProgress(runProgress progress.Log) progress.Log {
	if runProgress == nil {
		return progress.Discard()
	}
	return runProgress
}
