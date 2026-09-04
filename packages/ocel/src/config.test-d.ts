import { describe, it } from "vitest";

import { defineConfig } from "./config.js";

describe("an app's framework", () => {
  it("is required of a serverless app", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error a serverless app is packed per route, and the packer reads the framework
      apps: [{ name: "api", path: "services/api", compute: "serverless" }],
    });
  });

  it("is required of an app that names no compute", () => {
    defineConfig({
      slug: "shop",
      // @ts-expect-error an app that leaves compute to its provider may land on serverless
      apps: [{ name: "api", path: "services/api" }],
    });
  });

  it("is optional on a container app, whose build ignores it", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "api", path: "services/api", compute: "container" }],
    });
  });

  it("is still accepted on a container app", () => {
    defineConfig({
      slug: "shop",
      apps: [{ name: "api", path: "services/api", compute: "container", framework: "express" }],
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

  it("is refused on a serverless app, which builds no image", () => {
    defineConfig({
      slug: "shop",
      apps: [
        {
          name: "api",
          path: "services/api",
          framework: "express",
          compute: "serverless",
          // @ts-expect-error build configures a container image and there is none to configure here
          build: { dockerfile: "Dockerfile" },
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
