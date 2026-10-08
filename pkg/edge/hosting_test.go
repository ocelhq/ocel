package edge

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestHostingRoundTripsNeeds(t *testing.T) {
	t.Parallel()

	raw := `{"framework":"next","frameworkBuildId":"b1","edgeRouting":true,"needs":{` +
		`"edge-middleware":{"count":1,"matchers":["^/dashboard(?:/(.*))?$"]},` +
		`"edge-runtime":{"count":2,"routes":["/edgy","/api/stream"]},` +
		`"ppr-resume":{"count":1,"routes":["/"]},` +
		`"edge-cache":{"count":3},` +
		`"streaming":{"count":2}}}`

	var hosting Hosting
	if err := json.Unmarshal([]byte(raw), &hosting); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := Hosting{
		Framework:        "next",
		FrameworkBuildID: "b1",
		EdgeRouting:      true,
		Needs: map[Need]NeedDetail{
			NeedEdgeMiddleware: {Count: 1, Matchers: []string{"^/dashboard(?:/(.*))?$"}},
			NeedEdgeRuntime:    {Count: 2, Routes: []string{"/edgy", "/api/stream"}},
			NeedPPRResume:      {Count: 1, Routes: []string{"/"}},
			NeedEdgeCache:      {Count: 3},
			NeedStreaming:      {Count: 2},
		},
	}
	if !reflect.DeepEqual(hosting, want) {
		t.Fatalf("hosting = %+v, want %+v", hosting, want)
	}

	encoded, err := json.Marshal(hosting)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again Hosting
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatalf("unmarshal again: %v", err)
	}
	if !reflect.DeepEqual(again, hosting) {
		t.Fatalf("round trip = %+v, want %+v", again, hosting)
	}
}

func TestHostingKeepsAnUnknownNeedName(t *testing.T) {
	t.Parallel()

	var hosting Hosting
	if err := json.Unmarshal([]byte(`{"framework":"next","needs":{"time-travel":{"count":1}}}`), &hosting); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := hosting.Needs["time-travel"]; !ok {
		t.Fatalf("needs = %+v, want the unknown name kept for the origin to refuse", hosting.Needs)
	}
}
