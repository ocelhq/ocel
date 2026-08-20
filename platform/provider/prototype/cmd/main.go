package main

import (
	"context"
	"fmt"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	provider "github.com/ocelhq/ocel/platform/provider/contract"
	"github.com/ocelhq/ocel/platform/provider/kit"
	"github.com/ocelhq/ocel/platform/provider/prototype/awsshaped"
	"github.com/ocelhq/ocel/platform/provider/prototype/fake"
	"github.com/ocelhq/ocel/platform/provider/prototype/vpsshaped"
)

func main() {
	spec := provider.Spec{
		Slug: "nxtest", Class: edge.ClassProduction, Environment: "production", Tag: "v1",
		Apps: []provider.App{{
			Name: "web", Framework: "next", DeploymentID: "d-001", Folder: "apps/web",
			Build: provider.Build{Root: ".ocel/build/web", Routes: []provider.Route{{ID: "default", Entry: "server.js"}, {ID: "api", Entry: "api.js"}}},
		}},
		Resources: []provider.Resource{
			{Name: "main", Kind: provider.ResourcePostgres},
			{Name: "uploads", Kind: provider.ResourceBucket},
		},
		Usages: []provider.Usage{{App: "web", Resource: "main"}, {App: "web", Resource: "uploads"}},
	}

	run("aws", awsshaped.New("eu-west-1"), spec, []string{"isr"})
	run("vps", vpsshaped.New("box.example.com"), spec, nil)

	fmt.Println("\n== refusals come from the kit, before any port is touched ==")
	s := kit.New(vpsshaped.New("box.example.com"))
	bad := spec
	bad.Edge = "cloudfront"
	bad.Resources = append(bad.Resources, provider.Resource{Name: "events", Kind: "queue"})
	plan, _ := s.Preflight(context.Background(), bad)
	for _, r := range plan.Refusals {
		fmt.Printf("   ✕ %s — %s; %s\n", r.Subject, r.Reason, r.Fix)
	}
}

func run(name string, p provider.Provider, spec provider.Spec, features []string) {
	ctx := context.Background()
	s := kit.New(p)
	pr := fake.Printer{Prefix: "[" + name + "]"}
	fmt.Printf("\n== %s: bootstrap ==\n", name)
	must(func() error { _, err := s.Bootstrap(ctx, spec.Class, features, pr); return err })

	fmt.Printf("\n== %s: preflight ==\n", name)
	plan, err := s.Preflight(ctx, spec)
	must(func() error { return err })
	for _, c := range plan.Changes {
		fmt.Printf("   %-8s %s\n", c.Action, c.Subject)
	}

	fmt.Printf("\n== %s: deploy ==\n", name)
	res, err := s.Deploy(ctx, spec, pr)
	must(func() error { return err })
	fmt.Printf("   promoted %s; links:\n", res.PromotionID)
	for _, l := range res.Links {
		fmt.Printf("     %-8s %-8s values=%v secrets=%d\n", l.Kind, l.Resource, l.Values, len(l.Secrets))
	}

	fmt.Printf("\n== %s: second deploy (plan shows updates), then rollback to the first ==\n", name)
	spec.Tag = "v2"
	spec.Apps[0].DeploymentID = "d-002"
	res2, err := s.Deploy(ctx, spec, fake.Printer{Prefix: "[" + name + "]  "})
	must(func() error { return err })
	hist, _ := s.ListPromotions(ctx, spec.Slug, spec.Class)
	fmt.Printf("   history: ")
	for _, h := range hist {
		fmt.Printf("%s(%s active=%t) ", h.PromotionID, h.Tag, h.Active)
	}
	fmt.Println()
	must(func() error { return s.Rollback(ctx, spec.Slug, spec.Class, res.PromotionID) })
	hist, _ = s.ListPromotions(ctx, spec.Slug, spec.Class)
	fmt.Printf("   after rollback: active=%s (was %s)\n", hist[0].PromotionID, res2.PromotionID)

	fmt.Printf("\n== %s: add domain ==\n", name)
	must(func() error { return s.AddDomain(ctx, spec.Slug, "www.example.com", pr) })
	if _, ok := p.(provider.Certifying); !ok {
		fmt.Println("   (no Certificates port: the edge terminates TLS itself, so the kit skipped issuance)")
	}

	fmt.Printf("\n== %s: teardown refused while a project is deployed, then destroy, then teardown ==\n", name)
	if _, err := s.PlanTeardown(ctx, spec.Class); err != nil {
		fmt.Println("   ✕", err)
	}
	must(func() error { return s.DestroyProject(ctx, spec.Slug, spec.Class, pr) })
	must(func() error { return s.Teardown(ctx, spec.Class, pr) })
}

func must(f func() error) {
	if err := f(); err != nil {
		panic(err)
	}
}
