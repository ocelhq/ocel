package main

import "testing"

func TestARunAppHostNamesTheServiceItsTaskIsFor(t *testing.T) {
	tests := []struct {
		host                 string
		service, tag, region string
		ok                   bool
	}{
		{"ocel-shop-production-web-123456789.europe-west1.run.app", "ocel-shop-production-web", "", "europe-west1", true},
		{"r0000000a---ocel-shop-production-web-123456789.europe-west1.run.app", "ocel-shop-production-web", "r0000000a", "europe-west1", true},
		{"web-2-42.us-central1.run.app", "web-2", "", "us-central1", true},
		{"example.com", "", "", "", false},
		{"web.a.run.app.evil.example", "", "", "", false},
	}
	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			service, tag, region, ok := parseRunAppHost(test.host)

			if ok != test.ok || service != test.service || tag != test.tag || region != test.region {
				t.Errorf("parseRunAppHost(%q) = %q, %q, %q, %v; want %q, %q, %q, %v",
					test.host, service, tag, region, ok, test.service, test.tag, test.region, test.ok)
			}
		})
	}
}
