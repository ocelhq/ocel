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
		stage("build-web", "Building web"),
		stage("build-api", "Building api"),
		stage("provision", "Provisioning"),
		child("infra", "provision", "shared infrastructure"),
		child("app-web", "provision", "web"),
		child("app-api", "provision", "api"),
		stage("edge", "Reconciling the cloudfront edge"),
		stage("promote", "Promoting"),
	)

	s.declare(
		child("web-v1", "build-web", "[internal] load metadata for node:22-alpine"),
		child("web-v2", "build-web", "[build 4/9] RUN pnpm install --frozen-lockfile"),
		child("web-v3", "build-web", "[build 7/9] RUN pnpm build"),
		child("web-v4", "build-web", "exporting to image"),
		child("api-v1", "build-api", "[build 2/5] RUN go build -trimpath ./cmd/api"),
		child("api-v2", "build-api", "exporting to image"),
	)

	s.prog("build-web", "")
	s.prog("build-api", "")
	s.cached("web-v1", "")
	s.wait(350 * time.Millisecond)
	s.end("web-v1")
	s.prog("web-v2", "")
	s.prog("api-v1", "")
	s.log("web-v2", "Packages: +812", "Progress: resolved 1204, reused 1204, downloaded 0, added 812, done")
	s.wait(800 * time.Millisecond)
	s.end("web-v2")
	s.prog("web-v3", "")
	s.log("web-v3",
		"▲ Next.js 15.4.2",
		"   Creating an optimized production build ...",
		" ✓ Compiled successfully",
		"   Collecting page data ...",
		"   Generating static pages (28/28)",
	)
	s.wait(700 * time.Millisecond)
	s.end("api-v1")
	s.prog("api-v2", "")
	s.wait(400 * time.Millisecond)
	s.end("api-v2")
	s.end("build-api")
	s.wait(500 * time.Millisecond)
	s.end("web-v3")
	s.prog("web-v4", "")
	s.wait(400 * time.Millisecond)
	s.end("web-v4")
	s.end("build-web")

	s.prog("provision", "")
	s.prog("infra", "aws:ecr/Repository acme-prod/api")
	s.wait(500 * time.Millisecond)
	s.prog("infra", "aws:secretsmanager/SecretVersion acme-prod/env")
	s.wait(500 * time.Millisecond)
	s.end("infra")

	s.declare(
		child("web-img", "app-web", "ocel:artifact/EcrImage web:8f3c1a"),
		child("web-td", "app-web", "aws:ecs/TaskDefinition acme-prod-web"),
		child("web-svc", "app-web", "aws:ecs/Service acme-prod-web"),
		child("api-img", "app-api", "ocel:artifact/EcrImage api:8f3c1a"),
		child("api-role", "app-api", "aws:iam/Role acme-prod-api-task"),
		child("api-td", "app-api", "aws:ecs/TaskDefinition acme-prod-api"),
		child("api-svc", "app-api", "aws:ecs/Service acme-prod-api"),
	)

	s.prog("app-web", "")
	s.prog("app-api", "")
	s.prog("web-img", "")
	s.prog("api-img", "")
	for i := uint32(0); i <= 5; i++ {
		s.bar("web-img", "pushing 142 MB", i*28, 142)
		s.bar("api-img", "pushing 31 MB", i*6, 31)
		s.wait(200 * time.Millisecond)
	}
	s.end("web-img")
	s.end("api-img")

	s.prog("web-td", "")
	s.prog("api-role", "")
	s.wait(400 * time.Millisecond)
	s.end("web-td")
	s.end("api-role")
	s.prog("api-td", "")
	s.wait(300 * time.Millisecond)
	s.end("api-td")

	s.prog("web-svc", "1 of 2 tasks healthy")
	s.prog("api-svc", "0 of 2 tasks healthy")
	s.wait(900 * time.Millisecond)
	s.prog("web-svc", "2 of 2 tasks healthy")
	s.wait(500 * time.Millisecond)
	s.end("web-svc")
	s.end("app-web")

	if fail {
		s.prog("api-svc", "0 of 2 tasks healthy")
		s.wait(900 * time.Millisecond)
		s.failed("api-svc")
		s.failed("app-api")
		s.end("provision")
		s.result(&runui.Result{
			Headline: "Deploy failed",
			Error: "api: aws:ecs/Service acme-prod-api never reached a steady state\n" +
				"  task stopped: Essential container in task exited (exit 1)\n" +
				"  CannotPullContainerError is not the cause — the image pulled\n" +
				"web finished and is holding its new task set; nothing was promoted.",
			Withheld:   "Promotion withheld: the live deployment still serves every hostname.",
			Diagnostic: []string{"Replay:  ocel run replay --stage api"},
			StreamAt:   ".ocel/runs/2026-08-26T14-31-08Z.ndjson",
		})
		return s
	}

	s.wait(700 * time.Millisecond)
	s.prog("api-svc", "2 of 2 tasks healthy")
	s.wait(400 * time.Millisecond)
	s.end("api-svc")
	s.end("app-api")
	s.end("provision")

	s.prog("edge", "acme.example.com/api/*")
	s.wait(600 * time.Millisecond)
	s.end("edge")
	s.prog("promote", "")
	s.wait(700 * time.Millisecond)
	s.end("promote")

	s.result(&runui.Result{
		Success:  true,
		Headline: "Deployed acme to production",
		AppURLs:  []string{"https://acme.example.com"},
		StreamAt: ".ocel/runs/2026-08-26T14-31-08Z.ndjson",
	})
	return s
}
