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
