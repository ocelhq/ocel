package main

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/runui"
)

func awsContainerPlan() *runui.Plan {
	return &runui.Plan{
		Subject:  "Plan for acme production on aws (compute: container)",
		EdgeKind: "cloudfront",
		Groups: []runui.Group{{
			Kind: "shared",
			Name: "infrastructure",
			Rows: []runui.Row{
				{Kind: "aws:ec2/Vpc", Name: "acme-prod", Action: runui.Keep},
				{Kind: "aws:ecs/Cluster", Name: "acme-prod", Action: runui.Keep},
				{Kind: "aws:lb/LoadBalancer", Name: "acme-prod", Action: runui.Keep},
				{Kind: "aws:ecr/Repository", Name: "acme-prod/api", Action: runui.Create},
				{Kind: "aws:secretsmanager/SecretVersion", Name: "acme-prod/env", Action: runui.Update, Reason: "2 values changed"},
			},
		}, {
			Kind:    "app",
			Name:    "web",
			Feature: "nextjs",
			Rows: []runui.Row{
				{Kind: "ocel:artifact/EcrImage", Name: "web:8f3c1a", Action: runui.Create, Reason: "142 MB"},
				{Kind: "aws:ecs/TaskDefinition", Name: "acme-prod-web", Action: runui.Replace, Reason: "image digest changed"},
				{Kind: "aws:ecs/Service", Name: "acme-prod-web", Action: runui.Update, Slow: true},
				{Kind: "aws:lb/TargetGroup", Name: "acme-prod-web-8f3c", Action: runui.Create},
				{Kind: "aws:cloudwatch/LogGroup", Name: "/ocel/acme-prod/web", Action: runui.Keep},
			},
		}, {
			Kind:    "app",
			Name:    "api",
			Feature: "go",
			Rows: []runui.Row{
				{Kind: "ocel:artifact/EcrImage", Name: "api:8f3c1a", Action: runui.Create, Reason: "31 MB"},
				{Kind: "aws:ecs/TaskDefinition", Name: "acme-prod-api", Action: runui.Create},
				{Kind: "aws:ecs/Service", Name: "acme-prod-api", Action: runui.Create, Slow: true},
				{Kind: "aws:lb/TargetGroup", Name: "acme-prod-api-8f3c", Action: runui.Create},
				{Kind: "aws:iam/Role", Name: "acme-prod-api-task", Action: runui.Create},
				{Kind: "aws:cloudwatch/LogGroup", Name: "/ocel/acme-prod/api", Action: runui.Create},
			},
		}, {
			Kind: "edge",
			Name: "cloudfront",
			Rows: []runui.Row{
				{Kind: "route", Name: "acme.example.com/*", Action: runui.Keep},
				{Kind: "route", Name: "acme.example.com/api/*", Action: runui.Create},
			},
		}},
	}
}

func awsContainerApply(fail bool) *script {
	s := &script{}

	s.declare(
		unit("infra", "shared infrastructure"),
		sub("infra-prov", "infra", "provisioning"),

		unit("web", "web"),
		sub("web-build", "web", "building"),
		sub("web-prov", "web", "provisioning"),

		unit("api", "api"),
		sub("api-build", "api", "building"),
		sub("api-prov", "api", "provisioning"),

		unit("edge", "cloudfront edge"),
		sub("edge-rec", "edge", "reconciling"),

		unit("promo", "promotion"),
		sub("promo-go", "promo", "promoting"),
	)

	s.declare(
		sub("wb1", "web-build", "[internal] load metadata for node:22-alpine"),
		sub("wb2", "web-build", "[build 4/9] RUN pnpm install --frozen-lockfile"),
		sub("wb3", "web-build", "[build 7/9] RUN pnpm build"),
		sub("wb4", "web-build", "exporting to image"),

		sub("ab1", "api-build", "[build 2/5] RUN go build -trimpath ./cmd/api"),
		sub("ab2", "api-build", "exporting to image"),

		sub("if1", "infra-prov", "aws:ecr/Repository acme-prod/api"),
		sub("if2", "infra-prov", "aws:secretsmanager/SecretVersion acme-prod/env"),

		sub("wp1", "web-prov", "ocel:artifact/EcrImage web:8f3c1a"),
		sub("wp2", "web-prov", "aws:lb/TargetGroup acme-prod-web-8f3c"),
		sub("wp3", "web-prov", "aws:ecs/TaskDefinition acme-prod-web"),
		sub("wp4", "web-prov", "aws:ecs/Service acme-prod-web"),

		sub("ap1", "api-prov", "ocel:artifact/EcrImage api:8f3c1a"),
		sub("ap2", "api-prov", "aws:iam/Role acme-prod-api-task"),
		sub("ap3", "api-prov", "aws:ecs/TaskDefinition acme-prod-api"),
		sub("ap4", "api-prov", "aws:ecs/Service acme-prod-api"),
	)

	s.prog("infra", "")
	s.prog("infra-prov", "")
	s.prog("if1", "")
	s.prog("web", "")
	s.prog("web-build", "")
	s.prog("api", "")
	s.prog("api-build", "")

	s.cached("wb1")
	s.wait(350 * time.Millisecond)
	s.end("wb1")
	s.end("if1")
	s.prog("if2", "")

	s.prog("wb2", "")
	s.prog("ab1", "")
	s.log("wb2",
		"Progress: resolved 210, reused 0, downloaded 0\rProgress: resolved 1204, reused 1204, downloaded 0, added 812, done",
		"Packages: +812",
	)
	s.wait(500 * time.Millisecond)
	s.end("if2")
	s.end("infra-prov")
	s.end("infra")

	s.wait(400 * time.Millisecond)
	s.end("wb2")
	s.prog("wb3", "")
	s.log("wb3",
		"▲ Next.js 15.4.2",
		"   Creating an optimized production build ...",
		" ✓ Compiled successfully",
		"   Collecting page data ...",
		"   Generating static pages (28/28)",
		"",
		"Route (app)                     Size     First Load JS",
		"┌ ○ /                           1.2 kB        94.3 kB",
		"├ ● /blog/[slug]                  842 B        92.1 kB",
		"└ λ /api/revalidate               128 B        87.4 kB",
	)
	s.wait(400 * time.Millisecond)
	s.end("ab1")
	s.prog("ab2", "")
	s.wait(300 * time.Millisecond)
	s.end("ab2")
	s.end("api-build")

	s.prog("api-prov", "")
	s.prog("ap1", "")
	for i := uint32(0); i <= 4; i++ {
		s.bar("ap1", "pushing 31 MB", i*8, 31)
		s.wait(170 * time.Millisecond)
	}
	s.end("ap1")
	s.prog("ap2", "")
	s.wait(300 * time.Millisecond)
	s.end("ap2")
	s.prog("ap3", "")
	s.wait(250 * time.Millisecond)
	s.end("ap3")
	s.prog("ap4", "0 of 2 tasks healthy")

	s.wait(400 * time.Millisecond)
	s.end("wb3")
	s.prog("wb4", "")
	s.wait(350 * time.Millisecond)
	s.end("wb4")
	s.end("web-build")

	s.prog("web-prov", "")
	s.prog("wp1", "")
	for i := uint32(0); i <= 5; i++ {
		s.bar("wp1", "pushing 142 MB", i*28, 142)
		s.wait(180 * time.Millisecond)
	}
	s.end("wp1")
	s.prog("wp2", "")
	s.wait(250 * time.Millisecond)
	s.end("wp2")
	s.prog("wp3", "")
	s.wait(250 * time.Millisecond)
	s.end("wp3")
	s.prog("wp4", "1 of 2 tasks healthy")
	s.wait(600 * time.Millisecond)
	s.prog("wp4", "2 of 2 tasks healthy")
	s.wait(300 * time.Millisecond)
	s.end("wp4")
	s.end("web-prov")
	s.end("web")

	if fail {
		s.log("api-prov",
			"aws:ecs/Service acme-prod-api: waiting for steady state",
			"task 3f9a1c stopped: Essential container in task exited (exit 1)",
			"  container api exited with code 1",
			"  last 3 lines from /ocel/acme-prod/api:",
			"    panic: OCEL_DATABASE_URL is not set",
			"    goroutine 1 [running]:",
			"    main.main()",
		)
		s.wait(600 * time.Millisecond)
		s.failed("ap4")
		s.failed("api-prov")
		s.failed("api")
		s.result(&runui.Result{
			Headline: "Deploy failed",
			Error:    "api: aws:ecs/Service acme-prod-api never reached a steady state",
			Withheld: "Promotion withheld: the live deployment still serves every hostname.",
			Diagnostic: []string{
				"The api block above holds its full output.",
			},
			StreamAt: ".ocel/runs/2026-08-26T14-31-08Z.ndjson",
		})
		return s
	}

	s.prog("ap4", "2 of 2 tasks healthy")
	s.wait(400 * time.Millisecond)
	s.end("ap4")
	s.end("api-prov")
	s.end("api")

	s.prog("edge", "")
	s.prog("edge-rec", "acme.example.com/api/*")
	s.wait(600 * time.Millisecond)
	s.end("edge-rec")
	s.end("edge")

	s.prog("promo", "")
	s.prog("promo-go", "")
	s.wait(600 * time.Millisecond)
	s.end("promo-go")
	s.end("promo")

	s.result(&runui.Result{
		Success:  true,
		Headline: "Deployed acme to production",
		AppURLs:  []string{"https://acme.example.com"},
		StreamAt: ".ocel/runs/2026-08-26T14-31-08Z.ndjson",
	})
	return s
}
