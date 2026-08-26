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
		stage("build-web", "Building web"),
		stage("build-api", "Building api"),
		stage("provision", "Provisioning"),
		child("infra", "provision", "shared infrastructure"),
		child("app-web", "provision", "web"),
		child("app-api", "provision", "api"),
		stage("edge", "Reconciling the cloudfront edge"),
		stage("promote", "Promoting"),
	)

	s.prog("build-web", "")
	s.prog("build-api", "")
	s.log("build-web",
		"▲ Next.js 15.4.2",
		"   Creating an optimized production build ...",
		" ✓ Compiled successfully",
		"   Generating static pages (28/28)",
		"   Bundling server for the lambda target ...",
	)
	s.log("build-api", "go: building bootstrap for linux/arm64")
	s.wait(1200 * time.Millisecond)
	s.end("build-api")
	s.wait(600 * time.Millisecond)
	s.end("build-web")

	s.declare(
		child("web-server", "app-web", "aws:s3/BucketObjectv2 web/8f3c1a/server.zip"),
		child("web-assets", "app-web", "aws:s3/BucketObjectv2 web/8f3c1a/assets.tar"),
		child("web-pre", "app-web", "aws:s3/BucketObjectv2 web/8f3c1a/prerender.tar"),
		child("web-fn", "app-web", "aws:lambda/Function acme-prod-web"),
		child("web-alias", "app-web", "aws:lambda/Alias acme-prod-web:live"),
		child("web-gc", "app-web", "2 superseded objects"),
		child("api-zip", "app-api", "aws:s3/BucketObjectv2 api/8f3c1a/bootstrap.zip"),
		child("api-role", "app-api", "aws:iam/Role acme-prod-api-fn"),
		child("api-fn", "app-api", "aws:lambda/Function acme-prod-api"),
		child("api-route", "app-api", "aws:apigatewayv2/Route ANY /api/{proxy+}"),
	)

	s.prog("provision", "")
	s.prog("infra", "aws:secretsmanager/SecretVersion acme-prod/env")
	s.prog("app-web", "")
	s.prog("app-api", "")
	s.prog("web-server", "")
	s.prog("web-assets", "")
	s.prog("web-pre", "")
	s.prog("api-zip", "")
	s.prog("api-role", "")
	for i := uint32(0); i <= 4; i++ {
		s.bar("web-server", "uploading 18 MB", i*4, 18)
		s.bar("web-assets", "uploading 6 MB", i*2, 6)
		s.bar("api-zip", "uploading 9 MB", i*2, 9)
		s.wait(220 * time.Millisecond)
	}
	s.end("infra")
	s.end("web-server")
	s.end("web-assets")
	s.end("web-pre")
	s.end("api-zip")
	s.end("api-role")

	s.prog("web-fn", "")
	s.prog("api-fn", "")
	s.wait(600 * time.Millisecond)
	s.end("web-fn")
	s.end("api-fn")
	s.prog("web-alias", "")
	s.prog("api-route", "")
	s.wait(400 * time.Millisecond)
	s.end("web-alias")
	s.end("api-route")
	s.prog("web-gc", "")
	s.wait(300 * time.Millisecond)
	s.end("web-gc")
	s.end("app-web")
	s.end("app-api")
	s.end("provision")

	s.prog("edge", "invalidating /*")
	s.wait(1100 * time.Millisecond)
	s.end("edge")
	s.prog("promote", "")
	s.wait(500 * time.Millisecond)
	s.end("promote")

	s.result(&runui.Result{
		Success:  true,
		Headline: "Deployed acme to production",
		AppURLs:  []string{"https://acme.example.com"},
		StreamAt: ".ocel/runs/2026-08-26T14-44-52Z.ndjson",
	})
	return s
}
