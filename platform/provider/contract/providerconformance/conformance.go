package providerconformance

import (
	"context"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	provider "github.com/ocelhq/ocel/platform/provider/contract"
)

type Suite struct {
	New  func() provider.Provider
	Spec provider.Spec
}

func Run(t *testing.T, s Suite) {
	t.Helper()
	ctx := context.Background()

	t.Run("facts name a kind and a default edge the registry knows", func(t *testing.T) {
		p := s.New()
		f := p.Facts()
		if f.Kind == "" {
			t.Fatal("Facts().Kind is empty")
		}
		if _, err := p.Edges().For(ctx, f.DefaultEdge); err != nil {
			t.Fatalf("default edge %q: %v", f.DefaultEdge, err)
		}
	})

	t.Run("every membrane kind is also a served kind", func(t *testing.T) {
		f := s.New().Facts()
		for _, m := range f.Membrane {
			found := false
			for _, k := range f.Serves {
				found = found || k == m
			}
			if !found {
				t.Fatalf("%s crosses the membrane but is not served", m)
			}
		}
	})

	t.Run("the record store round-trips the adapter slot", func(t *testing.T) {
		p := s.New()
		want := provider.Record{Deploy: provider.State{Slug: "x", Class: edge.ClassProduction, Adapter: provider.Own(map[string]string{"k": "v"})}}
		if err := p.Records().Write(ctx, "x", edge.ClassProduction, want); err != nil {
			t.Fatal(err)
		}
		got, ok, err := p.Records().Read(ctx, "x", edge.ClassProduction)
		if err != nil || !ok {
			t.Fatalf("read: ok=%t err=%v", ok, err)
		}
		var m map[string]string
		if err := got.Deploy.Adapter.Into(&m); err != nil || m["k"] != "v" {
			t.Fatalf("adapter slot lost: %v %v", m, err)
		}
	})

	t.Run("plan is pure: two plans without an apply agree", func(t *testing.T) {
		p := s.New()
		a, err := p.Deployer().Plan(ctx, s.Spec, provider.State{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.Deployer().Plan(ctx, s.Spec, provider.State{})
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Changes) != len(b.Changes) {
			t.Fatalf("plan drifted without an apply: %d vs %d changes", len(a.Changes), len(b.Changes))
		}
	})

	t.Run("a deployment reopens from its own state", func(t *testing.T) {
		p := s.New()
		d := p.Deployer()
		plan, err := d.Plan(ctx, s.Spec, provider.State{})
		if err != nil {
			t.Fatal(err)
		}
		up, err := d.Upload(ctx, s.Spec, plan, silent{})
		if err != nil {
			t.Fatal(err)
		}
		dep, err := d.Apply(ctx, s.Spec, plan, up, provider.State{}, silent{})
		if err != nil {
			t.Fatal(err)
		}
		again, err := d.Open(dep.State())
		if err != nil {
			t.Fatal(err)
		}
		if len(again.Links()) != len(dep.Links()) {
			t.Fatalf("reopened deployment lost links: %d vs %d", len(again.Links()), len(dep.Links()))
		}
		if len(again.Records()) != len(s.Spec.Apps) {
			t.Fatalf("one record per app expected, got %d", len(again.Records()))
		}
	})

	t.Run("a certifying provider issues and describes", func(t *testing.T) {
		c, ok := s.New().(provider.Certifying)
		if !ok {
			t.Skip("edge terminates TLS itself")
		}
		cert, err := c.Certificates().Issue(ctx, []string{"www.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Certificates().Describe(ctx, cert.ID); err != nil {
			t.Fatal(err)
		}
	})
}

type silent struct{}

func (silent) Stage(provider.StageID, string, provider.StageID) {}
func (silent) Start(provider.StageID)                           {}
func (silent) Done(provider.StageID)                            {}
func (silent) Fail(provider.StageID, error)                     {}
func (silent) Say(provider.StageID, string)                     {}
func (silent) Count(provider.StageID, uint32, uint32)           {}
