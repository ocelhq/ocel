package deploy

import (
	"strings"
	"testing"
)

func TestPreviewHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		pointer   string
		app       string
		base      string
		singleApp bool
		want      string
	}{
		{"single app elides the app label", "pr-42-a1b2c3d4", "web", "preview.acme.com", true, "pr-42-a1b2c3d4.preview.acme.com"},
		{"multi app qualifies the label", "pr-42-a1b2c3d4", "web", "preview.acme.com", false, "pr-42-a1b2c3d4--web.preview.acme.com"},
		{"no pointer", "", "web", "preview.acme.com", true, ""},
		{"no base", "pr-42-a1b2c3d4", "web", "", true, ""},
		{"neither", "", "web", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := previewHost(tc.pointer, tc.app, tc.base, tc.singleApp); got != tc.want {
				t.Errorf("previewHost(%q, %q, %q, %v) = %q, want %q", tc.pointer, tc.app, tc.base, tc.singleApp, got, tc.want)
			}
		})
	}

	t.Run("the separator survives hyphenated halves", func(t *testing.T) {
		t.Parallel()

		host := previewHost("feat-a-b", "my-app", "preview.acme.com", false)
		label, _, _ := strings.Cut(host, ".")
		pointer, app, ok := strings.Cut(label, previewAppSeparator)
		if !ok || pointer != "feat-a-b" || app != "my-app" {
			t.Errorf("label %q splits to (%q, %q, %v), want (feat-a-b, my-app, true)", label, pointer, app, ok)
		}
	})

	t.Run("a max pointer fills the label", func(t *testing.T) {
		t.Parallel()

		pointer := strings.Repeat("p", previewLabelMaxLen)
		label, _, _ := strings.Cut(previewHost(pointer, "web", "preview.acme.com", true), ".")
		if len(label) != previewLabelMaxLen {
			t.Errorf("label %q is %d chars, want %d", label, len(label), previewLabelMaxLen)
		}
	})
}
