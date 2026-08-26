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
		unit("infra", "shared infrastructure"),
		sub("infra-prov", "infra", "provisioning"),

		unit("web", "web"),
		sub("web-build", "web", "building"),
		sub("web-push", "web", "pushing"),
		sub("web-prov", "web", "provisioning"),

		unit("api", "api"),
		sub("api-build", "api", "building"),
		sub("api-push", "api", "pushing"),
		sub("api-prov", "api", "provisioning"),

		unit("edge", "cloudflare edge"),
		sub("edge-rec", "edge", "reconciling"),

		unit("promo", "promotion"),
		sub("promo-go", "promo", "promoting"),
	)

	s.declare(
		sub("wb1", "web-build", "[internal] load build definition"),
		sub("wb2", "web-build", "[internal] load metadata for node:22-alpine"),
		sub("wb3", "web-build", "[build 3/9] RUN pnpm install --frozen-lockfile"),
		sub("wb4", "web-build", "[build 6/9] RUN pnpm build"),
		sub("wb5", "web-build", "exporting layers"),

		sub("ab1", "api-build", "[internal] load build definition"),
		sub("ab2", "api-build", "[build 2/5] RUN go build ./cmd/api"),
		sub("ab3", "api-build", "exporting layers"),

		sub("wp1", "web-prov", "container ocel-acme-web"),
		sub("wp2", "web-prov", "systemd unit ocel-acme-web.service"),
		sub("wp3", "web-prov", "removing container ocel-acme-web-4b2e"),

		sub("ap1", "api-prov", "container ocel-acme-api"),
		sub("ap2", "api-prov", "health check 127.0.0.1:8080"),
	)

	s.prog("infra", "")
	s.prog("infra-prov", "caddy site acme.example.com")
	s.prog("web", "")
	s.prog("web-build", "")
	s.prog("api", "")
	s.prog("api-build", "")

	s.cached("wb1")
	s.cached("wb2")
	s.cached("ab1")
	s.wait(400 * time.Millisecond)
	s.end("wb1")
	s.end("wb2")
	s.end("ab1")
	s.end("infra-prov")
	s.end("infra")

	s.prog("wb3", "")
	s.prog("ab2", "")
	s.wait(500 * time.Millisecond)
	s.log("wb3",
		"Lockfile is up to date, resolution step is skipped",
		"Progress: resolved 340, reused 0, downloaded 0\rProgress: resolved 912, reused 640, downloaded 44\rProgress: resolved 1204, reused 1204, downloaded 0, added 812, done",
		"Packages: +812",
		"dependencies:",
		"+ next 15.4.2",
		"+ react 19.1.0",
	)
	s.log("ab2", "go: downloading github.com/ocelhq/ocel/sdk v0.4.1")
	s.wait(700 * time.Millisecond)
	s.end("wb3")

	s.prog("wb4", "")
	s.log("wb4",
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
		"○  (Static)   prerendered as static content",
		"●  (SSG)      prerendered as static HTML",
		"λ  (Dynamic)  server-rendered on demand",
	)
	s.wait(400 * time.Millisecond)
	s.end("ab2")
	s.prog("ab3", "")
	s.wait(350 * time.Millisecond)
	s.end("ab3")
	s.end("api-build")

	s.prog("api-push", "acme/api:8f3c1a")
	for i := uint32(0); i <= 3; i++ {
		s.bar("api-push", "acme/api:8f3c1a", i, 3)
		s.wait(160 * time.Millisecond)
	}
	s.end("api-push")

	s.prog("api-prov", "")
	s.prog("ap1", "pulling acme/api:8f3c1a")
	s.wait(500 * time.Millisecond)
	s.end("wb4")
	s.prog("wb5", "")
	s.wait(400 * time.Millisecond)
	s.end("wb5")
	s.end("web-build")

	s.prog("web-push", "acme/web:8f3c1a")
	for i := uint32(0); i <= 6; i++ {
		s.bar("web-push", "acme/web:8f3c1a", i, 6)
		s.wait(150 * time.Millisecond)
	}
	s.end("web-push")

	s.end("ap1")
	s.prog("ap2", "attempt 1")
	s.prog("web-prov", "")
	s.prog("wp1", "pulling acme/web:8f3c1a")
	s.wait(600 * time.Millisecond)

	if fail {
		s.prog("ap2", "attempt 7")
		s.wait(700 * time.Millisecond)
		s.log("api-prov",
			"2026/08/26 14:22:58 listen tcp 0.0.0.0:8080: bind: address already in use",
			"2026/08/26 14:22:58 exit status 1",
		)
		s.failed("ap2")
		s.failed("api-prov")
		s.failed("api")
		s.end("wp1")
		s.prog("wp2", "")
		s.wait(300 * time.Millisecond)
		s.end("wp2")
		s.prog("wp3", "")
		s.wait(300 * time.Millisecond)
		s.end("wp3")
		s.end("web-prov")
		s.end("web")
		s.result(&runui.Result{
			Headline: "Deploy failed",
			Error:    "api never became healthy on 127.0.0.1:8080 after 7 attempts",
			Withheld: "web is built and staged but was not promoted — promotion needs every app.",
			Diagnostic: []string{
				"The api block above holds its full output.",
			},
			StreamAt: ".ocel/runs/2026-08-26T14-22-31Z.ndjson",
		})
		return s
	}

	s.prog("ap2", "attempt 2")
	s.wait(500 * time.Millisecond)
	s.end("ap2")
	s.end("api-prov")
	s.end("api")

	s.end("wp1")
	s.prog("wp2", "")
	s.wait(300 * time.Millisecond)
	s.end("wp2")
	s.prog("wp3", "")
	s.wait(300 * time.Millisecond)
	s.end("wp3")
	s.end("web-prov")
	s.end("web")

	s.prog("edge", "")
	s.prog("edge-rec", "api.acme.example.com")
	s.wait(600 * time.Millisecond)
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
		AppURLs:  []string{"https://acme.example.com", "https://api.acme.example.com"},
		StreamAt: ".ocel/runs/2026-08-26T14-22-31Z.ndjson",
	})
	return s
}
