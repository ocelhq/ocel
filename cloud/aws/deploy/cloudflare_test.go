package deploy

import "testing"

func TestZoneOwns(t *testing.T) {
	cases := []struct {
		hostname string
		zone     string
		want     bool
	}{
		{"app.acme.com", "acme.com", true},
		{"acme.com", "acme.com", true},
		{"app.acme.com", "app.acme.com", true},
		{"app.acme.com", "other.com", false},
		{"app.acme.com", "me.com", false},
		{"notacme.com", "acme.com", false},
		{"app.acme.com", "cme.com", false},
	}
	for _, tc := range cases {
		if got := zoneOwns(tc.hostname, tc.zone); got != tc.want {
			t.Errorf("zoneOwns(%q, %q) = %v, want %v", tc.hostname, tc.zone, got, tc.want)
		}
	}
}
