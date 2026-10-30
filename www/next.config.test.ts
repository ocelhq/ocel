import { getPathMatch } from "next/dist/shared/lib/router/utils/path-match";
import { compileNonPath } from "next/dist/shared/lib/router/utils/prepare-destination";
import { unstable_getResponseFromNextConfig } from "next/experimental/testing/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import nextConfig from "./next.config";

const respond = (url: string) =>
  unstable_getResponseFromNextConfig({ url: `https://ocel.dev${url}`, nextConfig });

const servedHeader = async (pathname: string, key: string) => {
  let value: string | null = null;
  for (const route of (await nextConfig.headers?.()) ?? []) {
    const params = getPathMatch(route.source, { strict: true, removeUnnamedParams: true })(
      pathname,
    );
    if (!params) continue;
    for (const header of route.headers) {
      if (header.key === key) value = compileNonPath(header.value, params);
    }
  }
  return value;
};

describe("the markdown of a docs page", () => {
  it("is served from the route that renders it", async () => {
    const response = await respond("/docs/telemetry.md");
    expect(response.headers.get("x-middleware-rewrite")).toBe(
      "https://ocel.dev/llms.mdx/docs/telemetry/content.md",
    );
  });

  it("keeps the dots of an error code in its slug", async () => {
    const response = await respond("/docs/errors/project.no_config.md");
    expect(response.headers.get("x-middleware-rewrite")).toBe(
      "https://ocel.dev/llms.mdx/docs/errors/project.no_config/content.md",
    );
  });

  it("carries no alternate link to itself", async () => {
    const response = await respond("/docs/errors/project.no_config.md");
    expect(response.headers.get("link")).toBeNull();
  });
});

describe("a docs page", () => {
  it("links the markdown at its own path as an alternate", async () => {
    expect(await servedHeader("/docs/errors/project.no_config", "link")).toBe(
      '</docs/errors/project.no_config.md>; rel="alternate"; type="text/markdown"',
    );
  });

  it("links a nested page's markdown at its full path", async () => {
    expect(await servedHeader("/docs/cli/deploy", "link")).toBe(
      '</docs/cli/deploy.md>; rel="alternate"; type="text/markdown"',
    );
  });

  it("links the root's markdown as index.md", async () => {
    const response = await respond("/docs");
    expect(response.headers.get("link")).toBe(
      '</docs/index.md>; rel="alternate"; type="text/markdown"',
    );
  });
});

describe("the analytics proxy", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("sends events to the region's ingest host", async () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_HOST", "https://us.i.posthog.com");
    const response = await respond("/ingest/e/");
    expect(response.headers.get("x-middleware-rewrite")).toBe("https://us.i.posthog.com/e");
  });

  it("serves the SDK's static files from the region's asset host", async () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_HOST", "https://eu.i.posthog.com");
    const response = await respond("/ingest/static/recorder.js");
    expect(response.headers.get("x-middleware-rewrite")).toBe(
      "https://eu-assets.i.posthog.com/static/recorder.js",
    );
  });

  it("sends everything to a self-hosted instance's own host", async () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_HOST", "https://posthog.example.com");
    const response = await respond("/ingest/static/recorder.js");
    expect(response.headers.get("x-middleware-rewrite")).toBe(
      "https://posthog.example.com/static/recorder.js",
    );
  });

  it("is absent without a PostHog host", async () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_HOST", "");
    const response = await respond("/ingest/e/");
    expect(response.headers.get("x-middleware-rewrite")).toBeNull();
  });
});
