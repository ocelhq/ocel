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

	if !ServedBy(" Sample ", sampleKind) {
		t.Error("a padded, differently cased header does not name the edge that sent it")
	}
	if ServedBy("other", sampleKind) {
		t.Error("another edge's header passed for the sample edge")
	}
	if ServedBy("", frontedKind) {
		t.Error("a missing header passed")
	}
}
