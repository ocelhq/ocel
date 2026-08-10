package deploy

const previewLabelMaxLen = 63

const previewAppSeparator = "--"

func previewHost(pointer, app, base string, singleApp bool) string {
	if pointer == "" || base == "" {
		return ""
	}
	if singleApp {
		return pointer + "." + base
	}
	return pointer + previewAppSeparator + app + "." + base
}
