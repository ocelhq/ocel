package connectorserver

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/ocelhq/ocel/pkg/consolecontract"
)

var contractBinding = consolecontract.Binding{
	Values: map[string]string{"connector.id": "0199b5c4-0000-7000-8000-00000000cafe"},
	Minted: []string{"connector.token"},
}

var contractBeats = []string{"connector-heartbeat", "connector-heartbeat-unknown"}

func TestEveryHeartbeatTheConnectorSendsMatchesAConsoleFixture(t *testing.T) {
	fixtures := consolecontract.ReadFixtures(t, "testdata/contract")
	var names []string
	for _, fixture := range fixtures {
		names = append(names, fixture.Name)
		if !slices.Contains(contractBeats, fixture.Name) {
			t.Errorf("fixture %s has no heartbeat", fixture.Name)
		}
	}
	for _, name := range contractBeats {
		if !slices.Contains(names, name) {
			t.Errorf("heartbeat %s has no fixture", name)
		}
	}

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			server := consolecontract.ServeFixture(t, fixture, contractBinding)
			heartbeat, _, _ := beating(t, server.URL)
			heartbeat.connectorID = contractBinding.Values["connector.id"]

			status, err := heartbeat.once(context.Background())
			if err != nil {
				t.Fatalf("once: %v", err)
			}
			if status != fixture.Response.Status {
				t.Errorf("status = %d, want %d", status, fixture.Response.Status)
			}
			requireHeartbeatToken(t, server.Minted("connector.token"), heartbeat, server.URL)
		})
	}
}

func requireHeartbeatToken(t *testing.T, raw string, heartbeat *beat, origin string) {
	t.Helper()
	public, err := base64.StdEncoding.DecodeString(heartbeat.identity.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.ParseString(raw,
		jwt.WithKey(jwa.EdDSA(), ed25519.PublicKey(public)),
		jwt.WithIssuer(heartbeat.connectorID),
		jwt.WithSubject(heartbeat.connectorID),
		jwt.WithAudience(origin),
	)
	if err != nil {
		t.Fatalf("the heartbeat token does not verify as the console verifies it: %v", err)
	}
	issued, _ := token.IssuedAt()
	expires, _ := token.Expiration()
	if lifetime := expires.Sub(issued); lifetime != 5*time.Minute {
		t.Errorf("the token lives %s, but the console accepts tokens at most 5m old", lifetime)
	}
}
