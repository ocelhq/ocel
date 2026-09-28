package edge

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestServeDescriptorRoundTripsNeeds(t *testing.T) {
	t.Parallel()

	raw := `{"framework":"next","buildId":"b1","edgeRouting":true,"needs":{` +
		`"edge-middleware":{"count":1,"matchers":["^/dashboard(?:/(.*))?$"]},` +
		`"edge-runtime":{"count":2,"routes":["/edgy","/api/stream"]},` +
		`"ppr-resume":{"count":1,"routes":["/"]},` +
		`"edge-cache":{"count":3},` +
		`"streaming":{"count":2}}}`

	var descriptor ServeDescriptor
	if err := json.Unmarshal([]byte(raw), &descriptor); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := ServeDescriptor{
		Framework:   "next",
		BuildID:     "b1",
		EdgeRouting: true,
		Needs: map[Need]NeedDetail{
			NeedEdgeMiddleware: {Count: 1, Matchers: []string{"^/dashboard(?:/(.*))?$"}},
			NeedEdgeRuntime:    {Count: 2, Routes: []string{"/edgy", "/api/stream"}},
			NeedPPRResume:      {Count: 1, Routes: []string{"/"}},
			NeedEdgeCache:      {Count: 3},
			NeedStreaming:      {Count: 2},
		},
	}
	if !reflect.DeepEqual(descriptor, want) {
		t.Fatalf("descriptor = %+v, want %+v", descriptor, want)
	}

	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again ServeDescriptor
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatalf("unmarshal again: %v", err)
	}
	if !reflect.DeepEqual(again, descriptor) {
		t.Fatalf("round trip = %+v, want %+v", again, descriptor)
	}
}

func TestServeDescriptorKeepsAnUnknownNeedName(t *testing.T) {
	t.Parallel()

	var descriptor ServeDescriptor
	if err := json.Unmarshal([]byte(`{"framework":"next","needs":{"time-travel":{"count":1}}}`), &descriptor); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := descriptor.Needs["time-travel"]; !ok {
		t.Fatalf("needs = %+v, want the unknown name kept for the origin to refuse", descriptor.Needs)
	}
}
