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

  it("names a server, and takes a username and the name of a password variable", () => {
    defineConfig({
      slug: "shop",
      registry: {
        server: "ghcr.io",
        username: "acme-bot",
        password: "GHCR_TOKEN",
      },
    });
  });

  it("takes no username, for a registry that authenticates on the token alone", () => {
    defineConfig({
      slug: "shop",
      registry: { server: "registry.fly.io", password: "FLY_TOKEN" },
    });
  });

  it("is refused without a server, which is the only thing naming where images land", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error a registry with no server names nowhere to push to
      registry: { password: "GHCR_TOKEN" },
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
          registry: { server: "ghcr.io", password: "GHCR_TOKEN" },
        },
      ],
    });
  });
});
