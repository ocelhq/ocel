package deploy

import (
	"strings"
	"testing"
)

func TestPreviewRouteSuffix_Length(t *testing.T) {
	if got := previewRouteSuffix("proj", "web"); len(got) != previewRouteSuffixLen {
		t.Errorf("previewRouteSuffix(%q, %q) = %q, length %d, want %d", "proj", "web", got, len(got), previewRouteSuffixLen)
	}
}

func TestPreviewRouteSuffix_Deterministic(t *testing.T) {
	first := previewRouteSuffix("proj", "web")
	second := previewRouteSuffix("proj", "web")
	if first != second {
		t.Errorf("previewRouteSuffix is not deterministic: %q then %q", first, second)
	}
}

func TestPreviewRouteSuffix_DistinctPerSlugAndApp(t *testing.T) {
	inputs := [][2]string{
		{"proj", "web"},
		{"proj", "api"},
		{"other", "web"},
		{"other", "api"},
	}
	seen := map[string][2]string{}
	for _, in := range inputs {
		suffix := previewRouteSuffix(in[0], in[1])
		if prev, ok := seen[suffix]; ok {
			t.Errorf("previewRouteSuffix collision %q for %v and %v", suffix, prev, in)
		}
		seen[suffix] = in
	}
}

// TestPreviewRouteSuffix_LengthDelimited is the reason the hashed fields are
// length-delimited: with plain concatenation these two apps share a zone and
// mint the same preview hostname.
func TestPreviewRouteSuffix_LengthDelimited(t *testing.T) {
	if a, b := previewRouteSuffix("a", "b-c"), previewRouteSuffix("a-b", "c"); a == b {
		t.Errorf("previewRouteSuffix(%q, %q) == previewRouteSuffix(%q, %q) == %q", "a", "b-c", "a-b", "c", a)
	}
}

func TestPreviewRouteSuffix_DNSLabelSafe(t *testing.T) {
	inputs := [][2]string{
		{"proj", "web"},
		{"Proj_UPPER", "My App"},
		{"", ""},
		{strings.Repeat("x", 200), strings.Repeat("y", 200)},
	}
	for _, in := range inputs {
		suffix := previewRouteSuffix(in[0], in[1])
		if !strings.HasPrefix(suffix, "-") {
			t.Errorf("previewRouteSuffix(%q, %q) = %q, want a leading hyphen", in[0], in[1], suffix)
		}
		for _, r := range suffix[1:] {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
				t.Errorf("previewRouteSuffix(%q, %q) = %q, has non-alphanumeric %q", in[0], in[1], suffix, r)
			}
		}
	}
}

func TestPreviewRouteHost(t *testing.T) {
	suffix := previewRouteSuffix("proj", "web")
	cases := []struct {
		name    string
		pointer string
		base    string
		want    string
	}{
		{"pointer and base", "pr-42-a1b2c3d4", "preview.acme.com", "pr-42-a1b2c3d4" + suffix + ".preview.acme.com"},
		{"no pointer", "", "preview.acme.com", ""},
		{"no base", "pr-42-a1b2c3d4", "", ""},
		{"neither", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := previewRouteHost("proj", "web", tc.pointer, tc.base); got != tc.want {
				t.Errorf("previewRouteHost(%q, %q) = %q, want %q", tc.pointer, tc.base, got, tc.want)
			}
		})
	}
}

// TestPreviewRouteHost_MaxPointerFillsLabel proves the length budget the suffix
// and previewid share: the longest pointer previewid can mint uses the DNS label
// exactly, with no room to spare.
func TestPreviewRouteHost_MaxPointerFillsLabel(t *testing.T) {
	pointer := strings.Repeat("p", previewPointerMaxLen)
	host := previewRouteHost("proj", "web", pointer, "preview.acme.com")
	label, _, ok := strings.Cut(host, ".")
	if !ok {
		t.Fatalf("previewRouteHost = %q, want a hostname with a label", host)
	}
	if len(label) != previewLabelMaxLen {
		t.Errorf("label %q is %d chars, want %d", label, len(label), previewLabelMaxLen)
	}
}
