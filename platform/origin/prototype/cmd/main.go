package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	origin "github.com/ocelhq/ocel/platform/origin/contract"
	"github.com/ocelhq/ocel/platform/origin/kit"
	"github.com/ocelhq/ocel/platform/origin/prototype/catalog"
)

type scenario struct {
	title    string
	topology origin.Topology
	app      origin.Requirements
}

func sel(kind origin.Kind) origin.Selection { return origin.Selection{Kind: kind} }

func main() {
	app := origin.Requirements{Resources: []origin.Requirement{
		{Role: origin.RoleBucket, Name: "uploads"},
		{Role: origin.RolePostgres, Name: "main"},
	}}
	scenarios := []scenario{
		{"aws, nothing configured", origin.Topology{Origin: sel(catalog.AWS)}, app},
		{"aws + cloudflare edge + cloudflare dns (today's next-test)", origin.Topology{Origin: sel(catalog.AWS), Edge: sel(catalog.CloudflareEdge), DNS: sel(catalog.CloudflareDNS)}, app},
		{"aws, postgres moved to neon", origin.Topology{Origin: sel(catalog.AWS), Resources: map[origin.Role]origin.Selection{origin.RolePostgres: sel(catalog.Neon)}}, app},
		{"cloudflare origin, nothing configured", origin.Topology{Origin: sel(catalog.Cloudflare)}, app},
		{"cloudflare origin + neon", origin.Topology{Origin: sel(catalog.Cloudflare), Resources: map[origin.Role]origin.Selection{origin.RolePostgres: sel(catalog.Neon)}}, app},
		{"cloudflare origin + aurora (aws-native)", origin.Topology{Origin: sel(catalog.Cloudflare), Resources: map[origin.Role]origin.Selection{origin.RolePostgres: sel(catalog.Aurora)}}, app},
		{"cloudflare origin + cloudfront edge (aws-native)", origin.Topology{Origin: sel(catalog.Cloudflare), Edge: sel(catalog.CloudFront)}, app},
		{"vps, nothing configured", origin.Topology{Origin: sel(catalog.VPS)}, app},
		{"vps, bucket on gcs by hmac keys (runtime speaks s3 only)", origin.Topology{Origin: sel(catalog.VPS), Resources: map[origin.Role]origin.Selection{origin.RoleBucket: sel(catalog.GCSKeys)}}, app},
		{"vps, bucket on s3 by keys", origin.Topology{Origin: sel(catalog.VPS), Resources: map[origin.Role]origin.Selection{origin.RoleBucket: sel(catalog.S3Keys)}}, app},
		{"gcp, cloudflare edge + dns", origin.Topology{Origin: sel(catalog.GCP), Edge: sel(catalog.CloudflareEdge), DNS: sel(catalog.CloudflareDNS)}, app},
	}

	origins := catalog.Origins()
	for i, s := range scenarios {
		fmt.Printf("\n%d. %s\n", i+1, s.title)
		host := kit.New(origins[s.topology.Origin.Kind], catalog.Independent()...)
		res, err := host.Deploy(context.Background(), s.topology, s.app, origin.DeploySpec{Slug: "demo", Class: origin.ClassProduction})
		printPlan(res.Plan)
		if err != nil {
			fmt.Printf("   refused: %s\n", strings.ReplaceAll(err.Error(), "\n", "\n            "))
			continue
		}
		for _, line := range res.Log {
			fmt.Printf("   %s\n", line)
		}
		var env map[string]string
		_ = res.State.Adapter.Into(&env)
		fmt.Printf("   app env: %v\n", env)
	}
}

func printPlan(p origin.Plan) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, f := range p.Fills {
		native := "independent"
		if f.Native {
			native = "native"
		}
		name := f.Name
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(w, "   %s\t%s\t%s\t%s\t%s\t%s\n", f.Role, name, f.Kind, f.Source, native, f.Protocol)
	}
	w.Flush()
}
