package gcp

import "github.com/ocelhq/ocel/pkg/progress"

func ensureProgress(runProgress progress.Progress) progress.Progress {
	if runProgress == nil {
		return progress.DiscardProgress()
	}
	return runProgress
}
