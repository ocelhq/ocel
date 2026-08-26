package main

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/runui"
)

func awsServerlessPlan() *runui.Plan {
	return &runui.Plan{
		Subject:  "Plan for acme production on aws (compute: serverless)",
		EdgeKind: "cloudfront",
		Groups: []runui.Group{{
			Kind: "shared",
			Name: "infrastructure",
			Rows: []runui.Row{
				{Kind: "aws:s3/Bucket", Name: "acme-prod-artifacts", Action: runui.Keep},
				{Kind: "aws:apigatewayv2/Api", Name: "acme-prod", Action: runui.Keep},
				{Kind: "aws:secretsmanager/SecretVersion", Name: "acme-prod/env", Action: runui.Update, Reason: "2 values changed"},
			},
		}, {
			Kind:    "app",
			Name:    "web",
			Feature: "nextjs",
			Rows: []runui.Row{
				{Kind: "aws:s3/BucketObjectv2", Name: "web/8f3c1a/server.zip", Action: runui.Create, Reason: "18 MB"},
				{Kind: "aws:s3/BucketObjectv2", Name: "web/8f3c1a/assets.tar", Action: runui.Create, Reason: "6 MB, 214 files"},
				{Kind: "aws:s3/BucketObjectv2", Name: "web/8f3c1a/prerender.tar", Action: runui.Create, Reason: "28 pages"},
				{Kind: "aws:lambda/Function", Name: "acme-prod-web", Action: runui.Update, Reason: "code digest changed"},
				{Kind: "aws:lambda/Alias", Name: "acme-prod-web:live", Action: runui.Update},
				{Kind: "aws:apigatewayv2/Route", Name: "ANY /{proxy+}", Action: runui.Keep},
				{Kind: "aws:s3/BucketObjectv2", Name: "web/7d1b02/server.zip", Action: runui.Delete, Reason: "3 deploys back"},
				{Kind: "aws:s3/BucketObjectv2", Name: "web/7d1b02/assets.tar", Action: runui.Delete, Reason: "3 deploys back"},
			},
		}, {
			Kind:    "app",
			Name:    "api",
			Feature: "go",
			Rows: []runui.Row{
				{Kind: "aws:s3/BucketObjectv2", Name: "api/8f3c1a/bootstrap.zip", Action: runui.Create, Reason: "9 MB"},
				{Kind: "aws:lambda/Function", Name: "acme-prod-api", Action: runui.Create},
				{Kind: "aws:lambda/Alias", Name: "acme-prod-api:live", Action: runui.Create},
				{Kind: "aws:iam/Role", Name: "acme-prod-api-fn", Action: runui.Create},
				{Kind: "aws:apigatewayv2/Route", Name: "ANY /api/{proxy+}", Action: runui.Create},
			},
		}, {
			Kind: "edge",
			Name: "cloudfront",
			Rows: []runui.Row{
				{Kind: "behaviour", Name: "/_next/static/*", Action: runui.Keep},
				{Kind: "behaviour", Name: "/api/*", Action: runui.Create},
				{Kind: "invalidation", Name: "/*", Action: runui.Create, Slow: true},
			},
		}},
	}
}

func awsServerlessApply() *script {
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
		sub("if1", "infra-prov", "aws:secretsmanager/SecretVersion acme-prod/env"),

		sub("wp1", "web-prov", "aws:s3/BucketObjectv2 web/8f3c1a/server.zip"),
		sub("wp2", "web-prov", "aws:s3/BucketObjectv2 web/8f3c1a/assets.tar"),
		sub("wp3", "web-prov", "aws:s3/BucketObjectv2 web/8f3c1a/prerender.tar"),
		sub("wp4", "web-prov", "aws:lambda/Function acme-prod-web"),
		sub("wp5", "web-prov", "aws:lambda/Alias acme-prod-web:live"),
		sub("wp6", "web-prov", "reclaiming 2 superseded objects"),

		sub("ap1", "api-prov", "aws:s3/BucketObjectv2 api/8f3c1a/bootstrap.zip"),
		sub("ap2", "api-prov", "aws:iam/Role acme-prod-api-fn"),
		sub("ap3", "api-prov", "aws:lambda/Function acme-prod-api"),
		sub("ap4", "api-prov", "aws:apigatewayv2/Route ANY /api/{proxy+}"),
	)

	s.prog("infra", "")
	s.prog("infra-prov", "")
	s.prog("if1", "")
	s.prog("web", "")
	s.prog("web-build", "")
	s.prog("api", "")
	s.prog("api-build", "")

	s.log("web-build",
		"▲ Next.js 15.4.2",
		"   Creating an optimized production build ...",
		" ✓ Compiled successfully",
		"   Generating static pages (28/28)",
		"   Bundling server for the lambda target ...",
		"",
		"Route (app)                     Size     First Load JS",
		"┌ ○ /                           1.2 kB        94.3 kB",
		"├ ● /blog/[slug]                  842 B        92.1 kB",
		"└ λ /api/revalidate               128 B        87.4 kB",
	)
	s.log("api-build", "go: building bootstrap for linux/arm64")
	s.wait(400 * time.Millisecond)
	s.end("if1")
	s.end("infra-prov")
	s.end("infra")
	s.wait(300 * time.Millisecond)
	s.end("api-build")

	s.prog("api-prov", "")
	s.prog("ap1", "")
	for i := uint32(0); i <= 3; i++ {
		s.bar("ap1", "uploading 9 MB", i*3, 9)
		s.wait(170 * time.Millisecond)
	}
	s.end("ap1")
	s.prog("ap2", "")
	s.wait(250 * time.Millisecond)
	s.end("ap2")

	s.wait(400 * time.Millisecond)
	s.end("web-build")

	s.prog("web-prov", "")
	s.prog("wp1", "")
	for i := uint32(0); i <= 4; i++ {
		s.bar("wp1", "uploading 18 MB", i*4, 18)
		s.wait(160 * time.Millisecond)
	}
	s.end("wp1")
	s.prog("wp2", "")
	s.wait(250 * time.Millisecond)
	s.end("wp2")

	s.prog("ap3", "")
	s.wait(300 * time.Millisecond)
	s.end("ap3")
	s.prog("ap4", "")
	s.wait(250 * time.Millisecond)
	s.end("ap4")
	s.end("api-prov")
	s.end("api")

	s.prog("wp3", "")
	s.wait(300 * time.Millisecond)
	s.end("wp3")
	s.prog("wp4", "")
	s.wait(350 * time.Millisecond)
	s.end("wp4")
	s.prog("wp5", "")
	s.wait(250 * time.Millisecond)
	s.end("wp5")
	s.prog("wp6", "")
	s.wait(250 * time.Millisecond)
	s.end("wp6")
	s.end("web-prov")
	s.end("web")

	s.prog("edge", "")
	s.prog("edge-rec", "invalidating /*")
	s.wait(1000 * time.Millisecond)
	s.end("edge-rec")
	s.end("edge")

	s.prog("promo", "")
	s.prog("promo-go", "")
	s.wait(500 * time.Millisecond)
	s.end("promo-go")
	s.end("promo")

	s.result(&runui.Result{
		Success:  true,
		Headline: "Deployed acme to production",
		AppURLs:  []string{"https://acme.example.com"},
		StreamAt: ".ocel/runs/2026-08-26T14-44-52Z.ndjson",
	})
	return s
}
