package conformance

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func RunEdges(t *testing.T, facts provider.Facts, edges provider.Edges) {
	t.Helper()

	supported := facts.Edges

	t.Run("Facts.Edges names each edge once", func(t *testing.T) {
		for i, kind := range supported {
			if kind == "" {
				t.Errorf("Facts.Edges[%d] is unnamed, and the CLI addresses an edge by its kind", i)
			}
			if slices.Index(supported, kind) != i {
				t.Errorf("Facts.Edges names %q twice", kind)
			}
		}
	})

	t.Run("Facts.DefaultEdge is no edge or one of the supported edges", func(t *testing.T) {
		if fallback := facts.DefaultEdge; fallback != edge.None && !slices.Contains(supported, fallback) {
			t.Errorf("Facts.DefaultEdge = %q, which Facts.Edges does not offer: %v", fallback, supported)
		}
	})

	t.Run("Open answers every edge a request can reach under the kind it was asked for", func(t *testing.T) {
		for _, kind := range listReachableEdges(facts) {
			front, err := edges.Open(kind)
			if err != nil {
				t.Errorf("Open(%q) = %v, want the edge Facts.Edges offers", kind, err)
				continue
			}
			if front == nil {
				t.Errorf("Open(%q) returned no edge and no error", kind)
				continue
			}
			if front.Kind() != kind {
				t.Errorf("Open(%q) answered an edge calling itself %q", kind, front.Kind())
			}
			for _, need := range front.Facts().Supported {
				if !edge.ValidNeed(need) {
					t.Errorf("Open(%q) supports %q, which is no need the contract names", kind, need)
				}
			}
		}
	})

	t.Run("an edge this provider does not serve is refused as invalid", func(t *testing.T) {
		unserved := edge.Kind("no-such-edge")
		if slices.Contains(supported, unserved) {
			t.Skip("this provider serves an edge by that name, so it is the wrong probe")
		}
		front, err := edges.Open(unserved)
		if err == nil {
			t.Fatalf("Open(%q) = %v, want a refusal", unserved, front)
		}
		requireInvalid(t, err, "Open")
	})
}

func listReachableEdges(facts provider.Facts) []edge.Kind {
	if slices.Contains(facts.Edges, facts.DefaultEdge) {
		return facts.Edges
	}
	return append([]edge.Kind{facts.DefaultEdge}, facts.Edges...)
}

func RunRouters(t *testing.T, facts provider.Facts, edges provider.Edges, routers provider.Routers) {
	t.Helper()

	t.Run("every edge a request can reach pairs with a router", func(t *testing.T) {
		for _, kind := range listReachableEdges(facts) {
			if len(facts.ListPairedRouters(kind)) == 0 {
				t.Errorf("Facts.Pairings pairs no router with the %q edge, so no app deployed through it could have its pointer moved", kind)
			}
		}
	})

	t.Run("a pairing names an edge a request can reach and the computes this provider runs, each once", func(t *testing.T) {
		for i, pairing := range facts.Pairings {
			if !slices.Contains(listReachableEdges(facts), pairing.Edge) {
				t.Errorf("Facts.Pairings[%d] names the %q edge, which no request reaches: Facts.Edges offers %v and Facts.DefaultEdge is %q", i, pairing.Edge, facts.Edges, facts.DefaultEdge)
			}
			if len(pairing.Computes) == 0 {
				t.Errorf("Facts.Pairings[%d] pairs the %q edge for no compute", i, pairing.Edge)
			}
			for _, compute := range pairing.Computes {
				if !slices.Contains(facts.Computes, compute) {
					t.Errorf("Facts.Pairings[%d] pairs the %q edge for %q, which Facts.Computes does not run: %v", i, pairing.Edge, compute, facts.Computes)
				}
				paired, _ := facts.PairedRouter(pairing.Edge, compute)
				if paired != pairing.Router {
					t.Errorf("the %q edge pairs %q apps with both %q and %q; an app routes through one router", pairing.Edge, compute, paired, pairing.Router)
				}
			}
		}
	})

	t.Run("a paired router opens under its kind and reaches every compute it is paired for", func(t *testing.T) {
		for _, pairing := range facts.Pairings {
			opened, err := routers.Open(pairing.Router)
			if err != nil {
				t.Errorf("Open(%q) = %v, want the router Facts.Pairings names", pairing.Router, err)
				continue
			}
			if opened.Kind() != pairing.Router {
				t.Errorf("Open(%q) answered a router calling itself %q", pairing.Router, opened.Kind())
			}
			routerFacts := opened.Facts()
			for _, compute := range pairing.Computes {
				switch {
				case compute == provider.ComputeServerless && !routerFacts.ReachesFunctions:
					t.Errorf("the %q router is paired for serverless apps and reaches no function", pairing.Router)
				case compute == provider.ComputeContainer && !routerFacts.ReachesContainers:
					t.Errorf("the %q router is paired for container apps and reaches no container", pairing.Router)
				}
			}
			front, err := edges.Open(pairing.Edge)
			if err != nil || opened.Hooks().Origin != nil {
				continue
			}
			if front.Facts().RunsCode && !routerFacts.SignsOriginForwards {
				t.Errorf("the %q router signs no origin forward, and the %q edge it pairs with runs code; the code it runs reaches the origin with the credentials it was bootstrapped, so its router must sign", pairing.Router, pairing.Edge)
			}
			if front.Facts().RunsCode && !routerFacts.Dispatches {
				t.Errorf("the %q router dispatches no path, and the %q edge it pairs with runs the code that dispatches a release's paths", pairing.Router, pairing.Edge)
			}
		}
	})

	t.Run("a router this provider does not have is refused as invalid, naming no router", func(t *testing.T) {
		missing := router.Kind("no-such-edge")
		opened, err := routers.Open(missing)
		if err == nil {
			t.Fatalf("Open(%q) = %v, want a refusal", missing, opened)
		}
		requireInvalid(t, err, "Open")
		if strings.Contains(strings.ToLower(err.Error()), "rout") {
			t.Errorf("Open(%q) = %v; a router is never user-facing, so no error names one", missing, err)
		}
	})
}

func RunDNS(t *testing.T, facts provider.Facts, dns provider.DNS) {
	t.Helper()

	supported := facts.DNSKinds

	t.Run("Facts.DNSKinds names each writer once", func(t *testing.T) {
		for i, kind := range supported {
			if kind == "" {
				t.Errorf("Facts.DNSKinds[%d] is unnamed, and a request selects a writer by its kind", i)
			}
			if slices.Index(supported, kind) != i {
				t.Errorf("Facts.DNSKinds names %q twice", kind)
			}
		}
	})

	t.Run("Open answers every supported writer", func(t *testing.T) {
		for _, kind := range supported {
			writer, err := dns.Open(kind, "conformance.invalid", "")
			if err != nil {
				t.Errorf("Open(%q) = %v, want the writer Facts.DNSKinds offers", kind, err)
				continue
			}
			if writer == nil {
				t.Errorf("Open(%q) returned no writer and no error", kind)
			}
		}
	})

	t.Run("a writer this provider does not have is refused as invalid", func(t *testing.T) {
		unserved := provider.DNSKind("no-such-dns")
		if slices.Contains(supported, unserved) {
			t.Skip("this provider writes dns by that name, so it is the wrong probe")
		}
		writer, err := dns.Open(unserved, "conformance.invalid", "")
		if err == nil {
			t.Fatalf("Open(%q) = %v, want a refusal", unserved, writer)
		}
		requireInvalid(t, err, "Open")
	})
}

func requireInvalid(t *testing.T, err error, call string) {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("%s() refused with %v, want a Refusal with code %s so the CLI can name the choices", call, err, refusal.CodeInvalid)
	}
	if refused.Message == "" {
		t.Errorf("%s() refused with no message, so the CLI has nothing to tell the user", call)
	}
}
