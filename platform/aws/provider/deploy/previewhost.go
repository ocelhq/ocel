package deploy

import edge "github.com/ocelhq/ocel/platform/edge/contract"

const (
	previewAppSeparator = edge.PreviewAppSeparator

	previewLabelMaxLen = edge.PreviewLabelMaxLen
)

func previewHost(pointer, app, base string, singleApp bool) string {
	if pointer == "" || base == "" {
		return ""
	}
	if singleApp {
		return pointer + "." + base
	}
	return pointer + previewAppSeparator + app + "." + base
}
