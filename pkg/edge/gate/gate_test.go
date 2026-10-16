package gate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
)

func TestTheGateAnswersEveryConformanceVector(t *testing.T) {
	t.Parallel()

	edgeconformance.RunGate(t)
}

func TestAPasswordHashesToTheValueTheVectorsStore(t *testing.T) {
	t.Parallel()

	const want = "f18e0cd484c475051052b9fed4f125e1db487c694fc74b1128b40bb4af41e5a3"
	if got := gate.HashPassword("test-preview-key", "JBSWY3DPEHPK3PXP2345"); got != want {
		t.Errorf("HashPassword = %q, want %q", got, want)
	}
}

func TestARevalidateTokenHashesToTheDigestTheVectorsStore(t *testing.T) {
	t.Parallel()

	const want = "8f0a88218e0b3a8c717eb9d90e7e0f2e12bddeb3121a11d9cdbae1737c08a384"
	if got := gate.HashRevalidateToken("b1946ac92492d2347c6235b4d2611184"); got != want {
		t.Errorf("HashRevalidateToken = %q, want %q", got, want)
	}
}

func TestAGateEncodesAsTheJSONTheEdgeContractFixtureRecords(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "platform", "edge", "contract", "fixtures", "gate.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Gates []struct {
			Name    string    `json:"name"`
			Gate    gate.Gate `json:"gate"`
			Encoded string    `json:"encoded"`
		} `json:"gates"`
		SharedEntry struct {
			Slug    string `json:"slug"`
			Encoded string `json:"encoded"`
		} `json:"sharedEntry"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	unlisted := func(listed gate.Gate) gate.Gate {
		if len(listed.BypassSecrets) == 0 {
			listed.BypassSecrets = nil
		}
		if len(listed.AllowOptions) == 0 {
			listed.AllowOptions = nil
		}
		return listed
	}
	for _, example := range fixture.Gates {
		t.Run(example.Name, func(t *testing.T) {
			encoded, err := json.Marshal(unlisted(example.Gate))
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != example.Encoded {
				t.Errorf("json.Marshal = %s, want %s", encoded, example.Encoded)
			}
		})
	}
	encoded, err := json.Marshal(map[string]gate.Gate{fixture.SharedEntry.Slug: unlisted(fixture.Gates[0].Gate)})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != fixture.SharedEntry.Encoded {
		t.Errorf("json.Marshal of the shared entry's gates = %s, want %s", encoded, fixture.SharedEntry.Encoded)
	}
}

func TestASignedSessionIsAdmittedOnTheHostItWasSignedForAndNoOther(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k", PasswordMAC: gate.HashPassword("k", "pw")}
	now := time.Unix(1_800_000_000, 0)
	cookie := gate.CookieName + "=" + held.SignSession("App.example.com", now)
	ask := func(host string) gate.Verdict {
		return held.Check(gate.Request{Method: "GET", Host: host, Target: "/", Header: gate.Header{"Cookie": {cookie}}}, now)
	}

	if verdict := ask("app.example.com"); verdict.Response != nil {
		t.Errorf("the signed session was refused with %d on the host it was signed for", verdict.Response.Status)
	}
	if verdict := ask("other.example.com"); verdict.Response == nil {
		t.Error("the signed session was admitted on another host")
	}
}
