package edge

import "testing"

func TestProbeHostname(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"*.preview.acme.com": LivenessProbeLabel + ".preview.acme.com",
		"app.acme.com":       "app.acme.com",
		"":                   "",
	} {
		if got := ProbeHostname(in); got != want {
			t.Errorf("ProbeHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServedBy(t *testing.T) {
	t.Parallel()

	if !ServedBy(" Cloudflare ", KindCloudflare) {
		t.Error("a padded, differently cased header does not name the edge that sent it")
	}
	if ServedBy("native", KindCloudflare) {
		t.Error("another edge's header passed for cloudflare")
	}
	if ServedBy("", KindNone) {
		t.Error("a missing header passed")
	}
}
