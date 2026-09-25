import { describe, it } from "vitest";

import { defineConfig } from "./config.js";

describe("an app's framework", () => {
  it("names what a serverless app is built with", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "api", path: "services/api", compute: "serverless", framework: "node" }],
    });
  });

  it("names an architecture alongside it", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "web",
          path: "apps/web",
          compute: "serverless",
          framework: "next",
          arch: "arm64",
        },
      ],
    });
  });

  it("is left off by a serverless app that lets ocel read it off the app", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "api", path: "services/api", compute: "serverless" }],
    });
  });

  it("is left off by an app that names no compute", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "api", path: "services/api" }],
    });
  });
});

describe("an app's build", () => {
  it("points a container app at a dockerfile outside its own directory", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          compute: "container",
          build: { dockerfile: "../shared/Dockerfile" },
        },
      ],
    });
  });

  it("takes the directory the image is built from, and the command that builds the app there", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          compute: "container",
          build: { context: ".", command: "turbo run build --filter=api" },
        },
      ],
    });
  });
});

describe("a project's registry", () => {
  it("is left off by a project that pushes nowhere of its own", () => {
    defineConfig({ slug: "shop" });
  });

  it("names a server, and takes a username and the placeholder of a password variable", () => {
    defineConfig({
      slug: "shop",
      registry: {
        server: "ghcr.io",
        username: "acme-bot",
        password: "${GHCR_TOKEN}",
      },
    });
  });

  it("takes no username, for a registry that authenticates on the token alone", () => {
    defineConfig({
      slug: "shop",
      registry: { server: "registry.fly.io", password: "${FLY_TOKEN}" },
    });
  });

  it("is refused without a server, which is the only thing naming where images land", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error a registry with no server names nowhere to push to
      registry: { password: "${GHCR_TOKEN}" },
    });
  });

  it("is refused without a password, so nothing falls back to an anonymous push", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error the push authenticates, and the variable holding its secret is named here
      registry: { server: "ghcr.io" },
    });
  });

  it("is a project's, never an app's", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          compute: "container",
          // @ts-expect-error one project pushes to one registry
          registry: { server: "ghcr.io", password: "${GHCR_TOKEN}" },
        },
      ],
    });
  });
});

describe("a project's provider", () => {
  it("is named alone when it needs no options", () => {
    defineConfig({ slug: "shop", provider: "aws" });
  });

  it("is keyed by its identifier, holding its options", () => {
    defineConfig({
      slug: "shop",
      provider: { gcp: { project: "acme-prod", region: "europe-west1" } },
    });
  });

  it("is refused keyed by two providers", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error a project deploys through one provider
      provider: { aws: {}, gcp: { project: "acme-prod", region: "europe-west1" } },
    });
  });

  it("is refused named alone when it cannot go without its options", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error gcp needs a project and a region
      provider: "gcp",
    });
  });

  it("is refused when ocel ships no such provider", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error azure is not a provider ocel ships
      provider: { azure: {} },
    });
  });

  it("is refused spelled as a name beside its options", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error the identifier is the key, not a name field
      provider: { name: "aws", options: {} },
    });
  });
});

describe("a project's edge", () => {
  it("is named alone", () => {
    defineConfig({ slug: "shop", edge: "cloudfront" });
  });

  it("is keyed by its identifier", () => {
    defineConfig({ slug: "shop", edge: { "api-gateway": {} } });
  });

  it("is refused keyed by two edges", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error a project is fronted by one edge
      edge: { cloudflare: {}, cloudfront: {} },
    });
  });

  it("is refused when no provider fronts with it", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error fastly is not an edge ocel fronts with
      edge: "fastly",
    });
  });

  it("is refused spelled as a kind", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error the identifier is the key, not a kind field
      edge: { kind: "cloudflare" },
    });
  });
});

describe("a project's dns", () => {
  it("is keyed by its identifier, holding its zone", () => {
    defineConfig({ slug: "shop", dns: { route53: { zone: "example.com" } } });
  });

  it("is named alone when it picks the zone itself", () => {
    defineConfig({ slug: "shop", dns: "cloudflare" });
  });

  it("is refused keyed by two dns services", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error records are written into one dns
      dns: { route53: {}, cloudflare: {} },
    });
  });

  it("is refused with a zone beside its identifier", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error the zone sits under the identifier
      dns: { kind: "route53", zone: "example.com" },
    });
  });
});
