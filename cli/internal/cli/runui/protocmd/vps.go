package main

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/runui"
)

func vpsPlan() *runui.Plan {
	return &runui.Plan{
		Subject:  "Plan for acme production on vps web-1",
		EdgeKind: "cloudflare",
		Groups: []runui.Group{{
			Kind: "shared",
			Name: "infrastructure",
			Rows: []runui.Row{
				{Kind: "docker network", Name: "ocel-acme", Action: runui.Keep},
				{Kind: "volume", Name: "ocel-acme-uploads", Action: runui.Keep},
				{Kind: "caddy site", Name: "acme.example.com", Action: runui.Update, Reason: "upstream port moved"},
			},
		}, {
			Kind:    "app",
			Name:    "web",
			Feature: "nextjs",
			Rows: []runui.Row{
				{Kind: "image", Name: "acme/web:8f3c1a", Action: runui.Create, Reason: "pushed to the host"},
				{Kind: "container", Name: "ocel-acme-web", Action: runui.Replace, Reason: "image changed"},
				{Kind: "systemd unit", Name: "ocel-acme-web.service", Action: runui.Update},
				{Kind: "container", Name: "ocel-acme-web-4b2e", Action: runui.Delete, Reason: "superseded by this deploy"},
			},
		}, {
			Kind:    "app",
			Name:    "api",
			Feature: "go",
			Rows: []runui.Row{
				{Kind: "image", Name: "acme/api:8f3c1a", Action: runui.Create, Reason: "pushed to the host"},
				{Kind: "container", Name: "ocel-acme-api", Action: runui.Replace, Reason: "image changed"},
				{Kind: "systemd unit", Name: "ocel-acme-api.service", Action: runui.Keep},
			},
		}, {
			Kind: "edge",
			Name: "cloudflare",
			Rows: []runui.Row{
				{Kind: "dns record", Name: "acme.example.com", Action: runui.Keep},
				{Kind: "dns record", Name: "api.acme.example.com", Action: runui.Create},
				{Kind: "origin rule", Name: "acme.example.com/api/*", Action: runui.Update},
			},
		}},
	}
}

func vpsApply(fail bool) *script {
	s := &script{}

	s.declare(
		stage("build-web", "Building web"),
		stage("build-api", "Building api"),
		stage("push", "Pushing images to web-1"),
		stage("provision", "Provisioning"),
		child("app-web", "provision", "web"),
		child("app-api", "provision", "api"),
		stage("edge", "Reconciling the cloudflare edge"),
		stage("promote", "Promoting"),
	)

	s.declare(
		child("web-v1", "build-web", "[internal] load build definition"),
		child("web-v2", "build-web", "[internal] load metadata for node:22-alpine"),
		child("web-v3", "build-web", "[build 3/9] RUN pnpm install --frozen-lockfile"),
		child("web-v4", "build-web", "[build 6/9] RUN pnpm build"),
		child("web-v5", "build-web", "exporting layers"),
		child("api-v1", "build-api", "[internal] load build definition"),
		child("api-v2", "build-api", "[build 2/5] RUN go build ./cmd/api"),
		child("api-v3", "build-api", "exporting layers"),
	)

	s.prog("build-web", "")
	s.prog("build-api", "")
	s.cached("web-v1", "")
	s.cached("web-v2", "")
	s.cached("api-v1", "")
	s.wait(400 * time.Millisecond)
	s.end("web-v1")
	s.end("web-v2")
	s.end("api-v1")

	s.prog("web-v3", "")
	s.prog("api-v2", "")
	s.wait(600 * time.Millisecond)
	s.log("web-v3",
		"Lockfile is up to date, resolution step is skipped",
		"Progress: resolved 1204, reused 1204, downloaded 0, added 0",
		"Packages: +812",
		"++++++++++++++++++++++++++++++++++++++++++++++++++",
		"Progress: resolved 1204, reused 1204, downloaded 0, added 812, done",
		"dependencies:",
		"+ next 15.4.2",
		"+ react 19.1.0",
	)
	s.log("api-v2",
		"go: downloading github.com/ocelhq/ocel/sdk v0.4.1",
		"go: downloading connectrpc.com/connect v1.18.1",
	)
	s.wait(900 * time.Millisecond)
	s.end("web-v3")

	s.prog("web-v4", "")
	s.log("web-v4",
		"▲ Next.js 15.4.2",
		"  Creating an optimized production build ...",
		" ✓ Compiled successfully",
		"   Linting and checking validity of types ...",
		"   Collecting page data ...",
		"   Generating static pages (0/28)",
		"   Generating static pages (14/28)",
		"   Generating static pages (28/28)",
		" ✓ Finalizing page optimization",
	)
	s.wait(700 * time.Millisecond)
	s.end("api-v2")
	s.prog("api-v3", "")
	s.wait(400 * time.Millisecond)
	s.end("api-v3")
	s.end("build-api")

	s.wait(500 * time.Millisecond)
	s.end("web-v4")
	s.prog("web-v5", "")
	s.wait(500 * time.Millisecond)
	s.end("web-v5")
	s.end("build-web")

	s.prog("push", "acme/web:8f3c1a")
	for i := uint32(0); i <= 6; i++ {
		s.bar("push", "acme/web:8f3c1a", i, 6)
		s.wait(180 * time.Millisecond)
	}
	s.prog("push", "acme/api:8f3c1a")
	for i := uint32(0); i <= 3; i++ {
		s.bar("push", "acme/api:8f3c1a", i, 3)
		s.wait(150 * time.Millisecond)
	}
	s.end("push")

	s.prog("provision", "")
	s.prog("app-web", "pulling acme/web:8f3c1a")
	s.prog("app-api", "pulling acme/api:8f3c1a")
	s.wait(700 * time.Millisecond)
	s.prog("app-web", "starting ocel-acme-web")
	s.prog("app-api", "starting ocel-acme-api")
	s.wait(600 * time.Millisecond)
	s.prog("app-web", "waiting for health check")
	s.prog("app-api", "waiting for health check")
	s.wait(900 * time.Millisecond)

	if fail {
		s.failed("app-api")
		s.wait(900 * time.Millisecond)
		s.prog("app-web", "removing ocel-acme-web-4b2e")
		s.wait(400 * time.Millisecond)
		s.end("app-web")
		s.end("provision")
		s.result(&runui.Result{
			Headline: "Deploy failed",
			Error:    "api: health check never passed on 127.0.0.1:8080 (12 attempts over 60s)\nthe running deployment is untouched",
			Withheld: "web deployed to its slot but was not promoted — promotion needs every app.",
			StreamAt: ".ocel/runs/2026-08-26T14-22-31Z.ndjson",
		})
		return s
	}

	s.end("app-api")
	s.prog("app-web", "removing ocel-acme-web-4b2e")
	s.wait(400 * time.Millisecond)
	s.end("app-web")
	s.end("provision")

	s.prog("edge", "api.acme.example.com")
	s.wait(700 * time.Millisecond)
	s.end("edge")

	s.prog("promote", "")
	s.wait(600 * time.Millisecond)
	s.end("promote")

	s.result(&runui.Result{
		Success:  true,
		Headline: "Deployed acme to production",
		AppURLs:  []string{"https://acme.example.com", "https://api.acme.example.com"},
		StreamAt: ".ocel/runs/2026-08-26T14-22-31Z.ndjson",
	})
	return s
}
