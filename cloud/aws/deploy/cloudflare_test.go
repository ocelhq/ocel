package deploy

import "testing"

func TestBuildScriptMultipartEnablesObservability(t *testing.T) {
	upload := WorkerUpload{
		ScriptName: "ocel-test",
		Main:       WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
	}

	meta := metadataFromMultipart(t, upload, "")
	obs, ok := meta["observability"].(map[string]any)
	if !ok {
		t.Fatalf("metadata has no observability object: %v", meta["observability"])
	}
	if obs["enabled"] != true {
		t.Errorf("observability.enabled = %v, want true", obs["enabled"])
	}
	logs, ok := obs["logs"].(map[string]any)
	if !ok || logs["enabled"] != true {
		t.Errorf("observability.logs not enabled: %v", obs["logs"])
	}
	traces, ok := obs["traces"].(map[string]any)
	if !ok || traces["enabled"] != true {
		t.Errorf("observability.traces not enabled: %v", obs["traces"])
	}
}

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
