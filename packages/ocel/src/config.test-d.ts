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

  it("carries no build context, which is always the app's own directory", () => {
    defineConfig({
      slug: "shop",
      apps: [
        // @ts-expect-error the context is the app directory and nothing configures it
        { name: "api", path: "services/api", compute: "container", build: { context: "../" } },
      ],
    });
  });
});
