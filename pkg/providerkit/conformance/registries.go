package conformance

import (
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func RunEdges(t *testing.T, facts providerkit.Facts, edges providerkit.Edges) {
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

func RunDNS(t *testing.T, facts providerkit.Facts, dns providerkit.DNS) {
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
		unserved := providerkit.DNSKind("no-such-dns")
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
		t.Fatalf("%s() refused with %v, want a Refusal carrying %s so the CLI can name the choices", call, err, refusal.CodeInvalid)
	}
	if refused.Message == "" {
		t.Errorf("%s() refused with no message, so the CLI has nothing to tell the user", call)
	}
}
