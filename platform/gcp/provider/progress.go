package gcp

import edge "github.com/ocelhq/ocel/platform/edge/contract"

func reporting(progress edge.Progress) edge.Progress {
	if progress == nil {
		return edge.DiscardProgress()
	}
	return progress
}
