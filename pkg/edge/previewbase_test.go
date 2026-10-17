package edge

import (
	"strings"
	"testing"
)

func TestAPreviewBaseNoResolverCouldAnswerUnderIsRefused(t *testing.T) {
	t.Parallel()

	for what, base := range map[string]string{
		"an empty base":                 "",
		"a wildcard for a base":         "*.preview.example.com",
		"a path where a name goes":      "preview/example.com",
		"a bare wildcard":               "*",
		"a dotless name":                "example",
		"a trailing dot":                "preview.example.com.",
		"an empty label":                "preview..example.com",
		"an underscore in a label":      "exam_ple.com",
		"a label opening with a hyphen": "-preview.example.com",
		"a label closing with a hyphen": "preview-.example.com",
		"a label over 63 bytes":         strings.Repeat("a", 64) + ".example.com",
		"a name the probe overflows":    strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("b", 55) + ".com",
	} {
		if err := RefuseUnusablePreviewBase(base); err == nil {
			t.Errorf("RefuseUnusablePreviewBase(%q) = nil (%s): a name no CA can ever issue for is an order that fails on every handshake for as long as the base is in place",
				base, what)
		}
	}
}

func TestAPreviewBaseOfDNSLabelsIsUsableInAnyCase(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"PREVIEW.Example.COM", strings.Repeat("a", 63) + ".example.com"} {
		if err := RefuseUnusablePreviewBase(base); err != nil {
			t.Errorf("RefuseUnusablePreviewBase(%q) = %v, want nil: a label is 63 bytes and a name is read case-insensitively", base, err)
		}
	}
}

func TestARefusedPreviewBaseNamesTheLimitsItBroke(t *testing.T) {
	t.Parallel()

	err := RefuseUnusablePreviewBase("exam_ple.com")
	want := `"exam_ple.com" is not a usable preview base: want dotted dns labels of ≤63 bytes, with ocel-edge-probe.exam_ple.com ≤253 bytes`
	if err == nil || err.Error() != want {
		t.Errorf("RefuseUnusablePreviewBase(%q) = %v, want %q", "exam_ple.com", err, want)
	}
}

func TestABaseTheOperatorTypedIsTheBaseTheyGet(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"203.0.113.10.sslip.io", "nip.io", "preview.acme.co.uk", "internal"} {
		usable := RefuseUnusablePreviewBase(base)
		if strings.Contains(base, ".") != (usable == nil) {
			t.Errorf("RefuseUnusablePreviewBase(%q) = %v: a base off the public suffix list shares a cookie origin with everything else under it, and that is a trade an operator makes knowingly by typing it — there is no vendor named here, no allowlist and no warning that ages",
				base, usable)
		}
	}
}
