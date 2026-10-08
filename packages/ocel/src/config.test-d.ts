import { describe, it } from "vitest";

import { defineConfig } from "./config.js";

describe("an app's compute", () => {
  it("is named alone", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "api", path: "services/api", compute: "container" }],
    });
  });

  it("is left off by an app that runs on what the provider runs", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "api", path: "services/api" }],
    });
  });

  it("is refused keyed by two computes", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          // @ts-expect-error an app runs on one compute
          compute: { serverless: {}, container: {} },
        },
      ],
    });
  });

  it("is refused when ocel runs no such compute", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error edge is no compute
      apps: [{ name: "api", path: "services/api", compute: "edge" }],
    });
  });
});

describe("a serverless app", () => {
  it("names what it is built with", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "api", path: "services/api", compute: { serverless: { framework: "node" } } }],
    });
  });

  it("names the main package of a go app", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          compute: { serverless: { framework: "go", entrypoint: "cmd/server" } },
        },
      ],
    });
  });

  it("names an architecture beside its compute", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "web",
          path: "apps/web",
          compute: { serverless: { framework: "next" } },
          arch: "arm64",
        },
      ],
    });
  });

  it("is refused a health check, since it runs no process to probe", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          // @ts-expect-error health gates a container release and nothing else
          compute: { serverless: { health: { path: "/up" } } },
        },
      ],
    });
  });

  it("is refused instance counts, since it scales itself", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          // @ts-expect-error instance counts size a container app and nothing else
          compute: { serverless: { instances: { min: 1 } } },
        },
      ],
    });
  });
});

describe("a container app", () => {
  it("points at a dockerfile outside its own directory", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          compute: { container: { image: { dockerfile: "../shared/Dockerfile" } } },
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
          compute: {
            container: { image: { context: ".", command: "turbo run build --filter=api" } },
          },
        },
      ],
    });
  });

  it("takes a health check and instance counts", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          compute: { container: { health: { path: "/up" }, instances: { min: 1, max: 4 } } },
        },
      ],
    });
  });

  it("is refused a framework, which ocel reads off the app", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          // @ts-expect-error a container runs the image it is given
          compute: { container: { framework: "next" } },
        },
      ],
    });
  });

  it("is refused an entrypoint, which the image decides", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          // @ts-expect-error the image's own command starts the app
          compute: { container: { entrypoint: "server.js" } },
        },
      ],
    });
  });

  it("is refused instance counts on the app itself", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          compute: "container",
          // @ts-expect-error instance counts sit under the container
          minInstances: 1,
        },
      ],
    });
  });
});

describe("an app's build", () => {
  it("goes without the resources it uses", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "web", path: "apps/web", buildWithResources: false }],
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
      // @ts-expect-error the push authenticates, and the variable containing its secret is named here
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

  it("is keyed by its identifier, mapping to its options", () => {
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

describe("a provider's edge", () => {
  it("is named alone", () => {
    defineConfig({ slug: "shop", provider: { aws: { edge: "cloudfront" } } });
  });

  it("is keyed by its identifier", () => {
    defineConfig({ slug: "shop", provider: { aws: { edge: { "api-gateway": {} } } } });
  });

  it("takes a tunnel keyed by cloudflare", () => {
    defineConfig({
      slug: "shop",
      provider: { vps: { ssh: "box", edge: { cloudflare: { tunnel: true } } } },
    });
  });

  it("is refused a tunnel keyed by an edge other than cloudflare", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error tunnel is an option of the cloudflare edge alone
      provider: { aws: { edge: { cloudfront: { tunnel: true } } } },
    });
  });

  it("is refused keyed by two edges", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error a project is fronted by one edge
      provider: { aws: { edge: { cloudflare: {}, cloudfront: {} } } },
    });
  });

  it("is refused when the provider cannot front deployments with it", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error cloudfront fronts aws alone
      provider: { gcp: { project: "acme-prod", region: "europe-west1", edge: "cloudfront" } },
    });
  });

  it("is refused when no provider fronts with it", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error fastly is not an edge ocel fronts with
      provider: { aws: { edge: "fastly" } },
    });
  });

  it("is refused spelled as a kind", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error the identifier is the key, not a kind field
      provider: { aws: { edge: { kind: "cloudflare" } } },
    });
  });

  it("is refused at the top of the config", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error the edge is the provider's
      edge: "cloudfront",
    });
  });
});

describe("a provider's dns", () => {
  it("is keyed by its identifier, mapping to its zone", () => {
    defineConfig({
      slug: "shop",
      provider: { aws: { dns: { route53: { zone: "example.com" } } } },
    });
  });

  it("is named alone when it picks the zone itself", () => {
    defineConfig({ slug: "shop", provider: { aws: { dns: "cloudflare" } } });
  });

  it("is refused keyed by two dns services", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error records are written into one dns
      provider: { aws: { dns: { route53: {}, cloudflare: {} } } },
    });
  });

  it("is refused when the provider cannot write records with it", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error route53 writes records for aws alone
      provider: { vps: { ssh: "box", dns: "route53" } },
    });
  });

  it("is refused with a zone beside its identifier", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error the zone sits under the identifier
      provider: { aws: { dns: { kind: "route53", zone: "example.com" } } },
    });
  });
});

describe("a config written as a program", () => {
  it("names no JSON Schema, which only a document an editor reads needs", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error defineConfig's own types are what the editor checks against
      $schema: "https://ocel.dev/schema.json",
    });
  });
});
