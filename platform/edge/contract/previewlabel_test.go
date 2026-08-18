package edge

import "testing"

func TestPreviewHost(t *testing.T) {
	t.Parallel()

	if got := PreviewHost("shop", "pr-12", "", "preview.acme.com"); got != "shop--pr-12.preview.acme.com" {
		t.Errorf("got %q, want the hostname every side of a preview deploy has to agree on", got)
	}
	if got := PreviewHost("shop", "pr-12", "web", "preview.acme.com"); got != "shop--pr-12--web.preview.acme.com" {
		t.Errorf("got %q, want the app qualified", got)
	}
	for _, args := range [][4]string{
		{"shop", "", "", "preview.acme.com"},
		{"shop", "pr-12", "", ""},
	} {
		if got := PreviewHost(args[0], args[1], args[2], args[3]); got != "" {
			t.Errorf("PreviewHost%v = %q, want nothing: a bare domain is not a preview hostname", args, got)
		}
	}
}
