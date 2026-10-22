package stackrecords_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestAHostnameIsReadyOnlyWhenItsLastProbeWasAnsweredByTheRouterItsAppPairsWith(t *testing.T) {
	t.Parallel()

	const front edge.Kind = "front"
	for what, c := range map[string]struct {
		host      stackrecords.HostnameState
		answering router.Kind
		ready     bool
	}{
		"answered by the paired router": {
			host:      stackrecords.HostnameState{Edge: front, Probe: stackrecords.ServeProbe{OK: true, Router: "relay"}},
			answering: "relay",
			ready:     true,
		},
		"answered by another router": {
			host:      stackrecords.HostnameState{Edge: front, Probe: stackrecords.ServeProbe{OK: true, Router: "direct"}},
			answering: "relay",
		},
		"answered by no router, with none paired": {
			host: stackrecords.HostnameState{Edge: front, Probe: stackrecords.ServeProbe{OK: true}},
		},
		"bound to another edge": {
			host:      stackrecords.HostnameState{Edge: "elsewhere", Probe: stackrecords.ServeProbe{OK: true, Router: "relay"}},
			answering: "relay",
		},
		"last probed unserved": {
			host:      stackrecords.HostnameState{Edge: front, Probe: stackrecords.ServeProbe{Router: "relay"}},
			answering: "relay",
		},
	} {
		state := stackrecords.EdgeState{Hosts: map[string]stackrecords.HostnameState{"app.acme.com": c.host}}
		if got := state.Ready("app.acme.com", front, c.answering); got != c.ready {
			t.Errorf("Ready() of a hostname %s = %v, want %v", what, got, c.ready)
		}
	}
}

func TestAProbeIsAnsweredByARouterOnlyWhenItReadThatRouterOffTheHostname(t *testing.T) {
	t.Parallel()

	if !(stackrecords.ServeProbe{Router: "relay"}).IsAnsweredBy("relay") {
		t.Error("a probe that read relay is not answered by relay")
	}
	if (stackrecords.ServeProbe{Router: "direct"}).IsAnsweredBy("relay") {
		t.Error("a probe that read direct is answered by relay")
	}
	if (stackrecords.ServeProbe{}).IsAnsweredBy("") {
		t.Error("a probe that read no router is answered by no router: an unnamed answer is nobody's")
	}
}

func TestTheRecordsAHumanMustWriteAreStoredUnderTheirLiteralName(t *testing.T) {
	t.Parallel()

	stored, err := json.Marshal(stackrecords.HostnameState{Manual: []edge.Record{{Name: "shop.example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"manual":`) {
		t.Errorf("a hostname record is stored as %s, want the manual records under %q", stored, "manual")
	}
}

func TestATierListsInOrderTheProjectsWhoseEdgeStateServesWhatTheCallerAsks(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	write := func(tier environment.Tier, slug string, value []byte) {
		t.Helper()
		if _, err := store.Write(t.Context(), records.Entry{Key: stackrecords.EdgeStackKey(tier, slug), Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	behind := func(kind edge.Kind) []byte {
		t.Helper()
		value, err := json.Marshal(stackrecords.EdgeState{Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	write(environment.TierProduction, "shop", behind("alb"))
	write(environment.TierProduction, "art", behind("alb"))
	write(environment.TierProduction, "blog", behind("cloudflare"))
	write(environment.TierPreview, "docs", behind("alb"))

	served, err := stackrecords.ProjectsServed(t.Context(), store, environment.TierProduction, func(state stackrecords.EdgeState) bool {
		return state.Kind == "alb"
	})
	if err != nil {
		t.Fatalf("ProjectsServed = %v", err)
	}
	if want := []string{"art", "shop"}; !slices.Equal(served, want) {
		t.Errorf("ProjectsServed = %v, want %v", served, want)
	}
}
