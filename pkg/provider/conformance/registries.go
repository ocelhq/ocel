package conformance

import (
	"errors"
	"slices"
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

	t.Run("Facts.DefaultEdge is one of the supported edges", func(t *testing.T) {
		fallback := facts.DefaultEdge
		switch {
		case fallback == "" && len(supported) == 0:
		case fallback == "":
			t.Errorf("Facts.DefaultEdge names no edge while Facts.Edges offers %v, so a request that names none has nowhere to go", supported)
		case !slices.Contains(supported, fallback):
			t.Errorf("Facts.DefaultEdge = %q, which Facts.Edges does not offer: %v", fallback, supported)
		}
	})

	t.Run("Open answers every supported edge under the kind it was asked for", func(t *testing.T) {
		for _, kind := range supported {
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

func RunRouters(t *testing.T, facts provider.Facts, edges provider.Edges, routers provider.Routers) {
	t.Helper()

	t.Run("every supported edge pairs with a router of its own kind", func(t *testing.T) {
		for _, kind := range facts.Edges {
			paired := slices.ContainsFunc(facts.Pairings, func(pairing provider.Pairing) bool { return pairing.Edge == kind })
			if !paired {
				t.Errorf("Facts.Pairings pairs no router with the %q edge, so no app deployed through it could be flipped", kind)
			}
		}
		for _, pairing := range facts.Pairings {
			if pairing.Router != router.Kind(pairing.Edge) {
				t.Errorf("Facts.Pairings pairs the %q edge with %q; every edge routes through a router of its own kind", pairing.Edge, pairing.Router)
			}
		}
	})

	t.Run("a pairing names a supported edge and the computes this provider runs, each once", func(t *testing.T) {
		for i, pairing := range facts.Pairings {
			if !slices.Contains(facts.Edges, pairing.Edge) {
				t.Errorf("Facts.Pairings[%d] names the %q edge, which Facts.Edges does not offer: %v", i, pairing.Edge, facts.Edges)
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
			routes, err := routers.Open(pairing.Router)
			if err != nil {
				t.Errorf("Open(%q) = %v, want the router Facts.Pairings names", pairing.Router, err)
				continue
			}
			if routes.Kind() != pairing.Router {
				t.Errorf("Open(%q) answered a router calling itself %q", pairing.Router, routes.Kind())
			}
			reaches := routes.Facts()
			for _, compute := range pairing.Computes {
				switch {
				case compute == provider.ComputeServerless && !reaches.ReachesFunctions:
					t.Errorf("the %q router is paired for serverless apps and reaches no function", pairing.Router)
				case compute == provider.ComputeContainer && !reaches.ReachesContainers:
					t.Errorf("the %q router is paired for container apps and reaches no container", pairing.Router)
				}
			}
			front, err := edges.Open(pairing.Edge)
			if err != nil {
				continue
			}
			if front.Facts().RunsCode && !reaches.SignsOriginForwards {
				t.Errorf("the %q router signs no origin forward, and the %q edge it pairs with runs code; the code it runs reaches the origin with the credentials it was bootstrapped, so its router must sign", pairing.Router, pairing.Edge)
			}
			if front.Facts().RunsCode && !reaches.Dispatches {
				t.Errorf("the %q router dispatches no path, and the %q edge it pairs with runs the code that dispatches a release's paths", pairing.Router, pairing.Edge)
			}
		}
	})

	t.Run("a router this provider does not have is refused as invalid", func(t *testing.T) {
		unserved := router.Kind("no-such-router")
		routes, err := routers.Open(unserved)
		if err == nil {
			t.Fatalf("Open(%q) = %v, want a refusal", unserved, routes)
		}
		requireInvalid(t, err, "Open")
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
