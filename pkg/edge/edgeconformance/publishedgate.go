package edgeconformance

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

type PublishedGate struct {
	Publish func(ctx context.Context, published *gate.Gate) error
	Read    func(ctx context.Context) (*gate.Gate, error)
	Signed  bool
}

func RunPublishedGate(t *testing.T, published PublishedGate) {
	t.Helper()

	const (
		host     = "shop.example.com"
		password = "correct horse battery"
	)
	now := time.Unix(1700000000, 0)
	gateWithKey := func(key string) gate.Gate {
		hash := gate.HashPassword(password)
		if published.Signed {
			hash = gate.SignPassword(key, password)
		}
		return gate.Gate{Key: key, PasswordHash: hash, BypassSecrets: []string{}, AllowOptions: []string{}}
	}
	publish := func(t *testing.T, toPublish *gate.Gate) {
		t.Helper()
		if err := published.Publish(t.Context(), toPublish); err != nil {
			t.Fatalf("Publish = %v", err)
		}
	}
	read := func(t *testing.T) *gate.Gate {
		t.Helper()
		held, err := published.Read(t.Context())
		if err != nil {
			t.Fatalf("Read = %v", err)
		}
		return held
	}
	ask := func(held *gate.Gate, header gate.Header) gate.Verdict {
		return held.Check(gate.Request{Method: "GET", Host: host, Target: "/", Header: header}, now)
	}

	t.Run("a published gate is the one a request is checked against", func(t *testing.T) {
		first := gateWithKey("first-key")
		publish(t, &first)

		got := read(t)
		if got == nil || !reflect.DeepEqual(*got, first) {
			t.Fatalf("the edge holds %+v, want the published gate, %+v", got, first)
		}
		if verdict := ask(got, gate.Header{}); verdict.Response == nil || verdict.Response.Status != 401 {
			t.Errorf("a request with no credentials = %+v, want it refused with 401", verdict.Response)
		}
		signed := first.SignSession(host, now)
		if verdict := ask(got, gate.Header{"Cookie": {gate.CookieName + "=" + signed}}); verdict.Response != nil {
			t.Errorf("a session signed with the published key was refused with %d", verdict.Response.Status)
		}
	})

	t.Run("publishing a rotated key refuses the sessions the old key signed", func(t *testing.T) {
		first, rotated := gateWithKey("first-key"), gateWithKey("rotated-key")
		publish(t, &first)
		oldSession := gate.CookieName + "=" + first.SignSession(host, now)
		publish(t, &rotated)

		got := read(t)
		if got == nil || got.Key != rotated.Key {
			t.Fatalf("the edge holds %+v, want the rotated gate, %+v", got, rotated)
		}
		if verdict := ask(got, gate.Header{"Cookie": {oldSession}}); verdict.Response == nil {
			t.Error("the session the old key signed still passes after the rotated gate was published")
		}
		if verdict := ask(got, gate.Header{"Cookie": {gate.CookieName + "=" + rotated.SignSession(host, now)}}); verdict.Response != nil {
			t.Errorf("a session signed with the rotated key was refused with %d", verdict.Response.Status)
		}
	})

	t.Run("publishing the same gate twice changes nothing", func(t *testing.T) {
		same := gateWithKey("first-key")
		publish(t, &same)
		publish(t, &same)

		if got := read(t); got == nil || !reflect.DeepEqual(*got, same) {
			t.Errorf("the edge holds %+v after two publications, want %+v", got, same)
		}
	})

	t.Run("publishing no gate opens the host", func(t *testing.T) {
		withdrawn := gateWithKey("first-key")
		publish(t, &withdrawn)
		publish(t, nil)

		if got := read(t); got != nil {
			t.Errorf("the edge still holds %+v after the gate was withdrawn", got)
		}
	})
}
