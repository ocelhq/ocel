package edgeconformance

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

type PublishedPreviewGate struct {
	Publish func(ctx context.Context, gate *previewgate.Gate) error
	Read    func(ctx context.Context) (*previewgate.Gate, error)
}

func RunPublishedPreviewGate(t *testing.T, published PublishedPreviewGate) {
	t.Helper()

	const (
		host     = "shop-abc.preview.example.com"
		password = "correct horse battery"
	)
	now := time.Unix(1700000000, 0)
	gateWithKey := func(key string) previewgate.Gate {
		return previewgate.Gate{Key: key, PasswordMAC: previewgate.HashPassword(key, password), BypassSecrets: []string{}, AllowOptions: []string{}}
	}
	publish := func(t *testing.T, gate *previewgate.Gate) {
		t.Helper()
		if err := published.Publish(t.Context(), gate); err != nil {
			t.Fatalf("Publish = %v", err)
		}
	}
	read := func(t *testing.T) *previewgate.Gate {
		t.Helper()
		gate, err := published.Read(t.Context())
		if err != nil {
			t.Fatalf("Read = %v", err)
		}
		return gate
	}
	ask := func(gate *previewgate.Gate, header previewgate.Header) previewgate.Verdict {
		return gate.Check(previewgate.Request{Method: "GET", Host: host, Target: "/", Header: header}, now)
	}

	t.Run("a published gate is the one a request is checked against", func(t *testing.T) {
		first := gateWithKey("first-key")
		publish(t, &first)

		got := read(t)
		if got == nil || !reflect.DeepEqual(*got, first) {
			t.Fatalf("the edge holds %+v, want the published gate, %+v", got, first)
		}
		if verdict := ask(got, previewgate.Header{}); verdict.Response == nil || verdict.Response.Status != 401 {
			t.Errorf("a request with no credentials = %+v, want it refused with 401", verdict.Response)
		}
		signed := first.SignSession(host, now)
		if verdict := ask(got, previewgate.Header{"Cookie": {previewgate.CookieName + "=" + signed}}); verdict.Response != nil {
			t.Errorf("a session signed with the published key was refused with %d", verdict.Response.Status)
		}
	})

	t.Run("publishing a rotated key refuses the sessions the old key signed", func(t *testing.T) {
		first, rotated := gateWithKey("first-key"), gateWithKey("rotated-key")
		publish(t, &first)
		oldSession := previewgate.CookieName + "=" + first.SignSession(host, now)
		publish(t, &rotated)

		got := read(t)
		if got == nil || got.Key != rotated.Key {
			t.Fatalf("the edge holds %+v, want the rotated gate, %+v", got, rotated)
		}
		if verdict := ask(got, previewgate.Header{"Cookie": {oldSession}}); verdict.Response == nil {
			t.Error("the session the old key signed still passes after the rotated gate was published")
		}
		if verdict := ask(got, previewgate.Header{"Cookie": {previewgate.CookieName + "=" + rotated.SignSession(host, now)}}); verdict.Response != nil {
			t.Errorf("a session signed with the rotated key was refused with %d", verdict.Response.Status)
		}
	})

	t.Run("publishing the same gate twice changes nothing", func(t *testing.T) {
		gate := gateWithKey("first-key")
		publish(t, &gate)
		publish(t, &gate)

		if got := read(t); got == nil || !reflect.DeepEqual(*got, gate) {
			t.Errorf("the edge holds %+v after two publications, want %+v", got, gate)
		}
	})

	t.Run("publishing no gate opens the preview", func(t *testing.T) {
		gate := gateWithKey("first-key")
		publish(t, &gate)
		publish(t, nil)

		if got := read(t); got != nil {
			t.Errorf("the edge still holds %+v after the gate was withdrawn", got)
		}
	})
}
