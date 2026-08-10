package deploy

// previewLabelMaxLen is the DNS label limit (RFC 1035). Spelled again as
// previewid.maxLabelLen in the CLI, which caps the pointer alone; this one
// measures the whole assembled label.
const previewLabelMaxLen = 63

// previewAppSeparator joins the pointer and the app in a preview hostname's
// label. It is two hyphens because either half may contain one of its own, so a
// single hyphen would leave the split ambiguous.
//
// Keep in step with previewid.appSeparator in the CLI, which refuses a pointer
// carrying it, and APP_SEPARATOR in workers/nextjs/src/preview.ts, which parses
// the label back: three modules, no shared constant.
const previewAppSeparator = "--"

// previewHost is the hostname a preview pointer is served on under a project's
// preview base domain: "<pointer>.<base>" when the project has a single app —
// there is nothing to disambiguate — and "<pointer>--<app>.<base>" otherwise.
// The project's entrypoint worker parses the label back by the same grammar, so
// the two must agree. base is the base domain with the leading "*." already
// stripped (see previewBaseDomain). Returns "" when either half is missing,
// leaving no usable hostname.
func previewHost(pointer, app, base string, singleApp bool) string {
	if pointer == "" || base == "" {
		return ""
	}
	if singleApp {
		return pointer + "." + base
	}
	return pointer + previewAppSeparator + app + "." + base
}
