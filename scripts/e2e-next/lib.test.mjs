import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";

import {
  APP_NAME,
  DNS_LABEL,
  ISR_REVALIDATE_SECONDS,
  ISR_ROUTE,
  MAX_SLUG_LEN,
  buildBaselineManifest,
  deployURL,
  isrToken,
  lambdaLogGroups,
  markerLines,
  mergeBaselineManifest,
  projectSlug,
  projectSlugForApp,
  renderOcelConfig,
  suiteFromResultsPath,
  suiteResultFromJest,
  tail,
  withBuildScript,
} from "./lib.mjs";

describe("projectSlug", () => {
  it("is a valid single DNS label carrying the run id", () => {
    const slug = projectSlug({ runId: "1234567890", dir: "/tmp/next-e2e-abc" });
    expect(slug).toMatch(DNS_LABEL);
    expect(slug).toContain("1234567890");
    expect(slug.length).toBeLessThanOrEqual(MAX_SLUG_LEN);
  });

  it("gives two temp apps in the same run their own project", () => {
    const a = projectSlug({ runId: "7", dir: "/tmp/next-e2e-a" });
    const b = projectSlug({ runId: "7", dir: "/tmp/next-e2e-b" });
    expect(a).not.toBe(b);
  });

  it("is stable for the same run id and directory", () => {
    expect(projectSlug({ runId: "7", dir: "/tmp/x" })).toBe(projectSlug({ runId: "7", dir: "/tmp/x" }));
  });

  it("stays a valid label with no run id and a hostile directory name", () => {
    const slug = projectSlug({ runId: "", dir: "/tmp/Next E2E_App/../weird" });
    expect(slug).toMatch(DNS_LABEL);
  });

  it("stays within the slug budget for an absurdly long run id", () => {
    const slug = projectSlug({ runId: "9".repeat(200), dir: "/tmp/x" });
    expect(slug).toMatch(DNS_LABEL);
    expect(slug.length).toBeLessThanOrEqual(MAX_SLUG_LEN);
  });
});

describe("projectSlugForApp", () => {
  it("prefers NEXT_TEST_DIR over the app directory, so deploy and cleanup agree", () => {
    vi.stubEnv("GITHUB_RUN_ID", "42");
    vi.stubEnv("NEXT_TEST_DIR", "/tmp/harness-app");
    expect(projectSlugForApp("/somewhere/else")).toBe(projectSlug({ runId: "42", dir: "/tmp/harness-app" }));
  });

  it("falls back to the app directory when the harness sets no NEXT_TEST_DIR", () => {
    vi.stubEnv("GITHUB_RUN_ID", "42");
    vi.stubEnv("NEXT_TEST_DIR", "");
    expect(projectSlugForApp("/tmp/app")).toBe(projectSlug({ runId: "42", dir: "/tmp/app" }));
  });

  it("derives the same slug twice, so cleanup can recover it without the state file", () => {
    vi.stubEnv("GITHUB_RUN_ID", "42");
    vi.stubEnv("NEXT_TEST_DIR", "/tmp/harness-app");
    expect(projectSlugForApp("/tmp/harness-app")).toBe(projectSlugForApp("/tmp/harness-app"));
  });
});

describe("renderOcelConfig", () => {
  const config = renderOcelConfig({ slug: "e2e-42-abcd1234", previewDomain: "*.e2e.example.com" });

  it("carries this temp app's own project slug, the provider and the preview wildcard", () => {
    expect(config).toContain(`slug: "e2e-42-abcd1234"`);
    expect(config).toContain("awsProvider()");
    expect(config).toContain(`preview: "*.e2e.example.com"`);
  });

  it("declares one app explicitly, under the constant app name", () => {
    expect(APP_NAME).toBe("app");
    expect(config).toContain(`apps: [{ name: "app", path: ".", framework: "next" }]`);
  });

  it("is pure, so cleanup re-renders byte-for-byte what deploy wrote", () => {
    vi.stubEnv("GITHUB_RUN_ID", "42");
    vi.stubEnv("NEXT_TEST_DIR", "/tmp/harness-app");
    const args = { slug: projectSlugForApp("/tmp/harness-app"), previewDomain: "*.e2e.example.com" };
    expect(renderOcelConfig(args)).toBe(renderOcelConfig(args));
    expect(renderOcelConfig(args)).toContain(`slug: ${JSON.stringify(args.slug)}`);
  });

  it("omits the domains block when no preview domain is configured", () => {
    expect(renderOcelConfig({ slug: "s" })).not.toContain("domains");
  });
});

describe("withBuildScript", () => {
  it("adds the next build script buildNext requires", () => {
    expect(withBuildScript({ name: "app" })).toEqual({ name: "app", scripts: { build: "next build" } });
  });

  it("leaves an existing build script alone", () => {
    const pkg = { scripts: { build: "next build --turbo", dev: "next dev" } };
    expect(withBuildScript(pkg)).toEqual(pkg);
  });

  it("does not mutate its input", () => {
    const pkg = { name: "app" };
    withBuildScript(pkg);
    expect(pkg).toEqual({ name: "app" });
  });
});

describe("deployURL", () => {
  it("takes the first featured app URL", () => {
    expect(deployURL({ appUrls: ["https://a.example.com", "https://b.example.com"] })).toBe("https://a.example.com");
  });

  it("refuses a result with no URL, naming what it read", () => {
    expect(() => deployURL({ appUrls: [] })).toThrow(/no app URL/i);
    expect(() => deployURL({})).toThrow(/no app URL/i);
  });
});

describe("isrToken", () => {
  it("reads the probe page's per-render token", () => {
    expect(isrToken('<p id="isr-token">isr-token:1769000000000</p>')).toBe("1769000000000");
  });

  it("is null when the response is not the probe page", () => {
    expect(isrToken("<h1>500</h1>")).toBeNull();
    expect(isrToken("")).toBeNull();
    expect(isrToken(undefined)).toBeNull();
  });

  it("matches the marker the smoke app's page emits", () => {
    const page = readFileSync(new URL("./smoke-app/app/isr/page.tsx", import.meta.url), "utf8");
    expect(page).toContain("isr-token:");
    expect(page).toContain(`export const revalidate = ${ISR_REVALIDATE_SECONDS};`);
    expect(ISR_ROUTE).toBe("/isr");
  });
});

describe("markerLines", () => {
  it("emits the three harness markers in order", () => {
    expect(markerLines({ buildId: "bld", promotionId: "prm" })).toEqual([
      "BUILD_ID: bld",
      "DEPLOYMENT_ID: prm",
      "IMMUTABLE_ASSET_TOKEN: undefined",
    ]);
  });

  it("reports a missing value as undefined rather than blank", () => {
    expect(markerLines({})).toEqual([
      "BUILD_ID: undefined",
      "DEPLOYMENT_ID: undefined",
      "IMMUTABLE_ASSET_TOKEN: undefined",
    ]);
  });
});

describe("lambdaLogGroups", () => {
  it("maps tagged function ARNs to their log groups", () => {
    const groups = lambdaLogGroups({
      ResourceTagMappingList: [
        { ResourceARN: "arn:aws:lambda:us-east-1:1:function:proj--web-abc123" },
        { ResourceARN: "arn:aws:lambda:us-east-1:1:function:proj--web-def456" },
      ],
    });
    expect(groups).toEqual(["/aws/lambda/proj--web-abc123", "/aws/lambda/proj--web-def456"]);
  });

  it("ignores non-function ARNs and an empty response", () => {
    expect(lambdaLogGroups({})).toEqual([]);
    expect(lambdaLogGroups({ ResourceTagMappingList: [{ ResourceARN: "arn:aws:s3:::bucket" }] })).toEqual([]);
  });
});

describe("tail", () => {
  it("keeps only the last n lines", () => {
    expect(tail("a\nb\nc\nd", 2)).toBe("c\nd");
  });

  it("returns short input unchanged", () => {
    expect(tail("a\nb", 5)).toBe("a\nb");
    expect(tail("", 5)).toBe("");
  });
});

describe("suiteFromResultsPath", () => {
  it("recovers the harness's test-file key", () => {
    expect(suiteFromResultsPath("test/e2e/app-dir/app/index.test.ts.results.json")).toBe(
      "test/e2e/app-dir/app/index.test.ts",
    );
  });

  it("normalizes windows separators and a leading ./", () => {
    expect(suiteFromResultsPath("./test\\e2e\\app\\index.test.ts.results.json")).toBe("test/e2e/app/index.test.ts");
  });
});

describe("suiteResultFromJest", () => {
  it("splits assertions into passed and failed by full case name", () => {
    expect(
      suiteResultFromJest({
        testResults: [
          {
            assertionResults: [
              { ancestorTitles: ["app dir"], title: "renders", status: "passed" },
              { ancestorTitles: ["app dir"], title: "revalidates", status: "failed" },
              { ancestorTitles: [], title: "skipped one", status: "pending" },
            ],
          },
        ],
      }),
    ).toEqual({
      passed: ["app dir > renders"],
      failed: ["app dir > revalidates"],
      flakey: [],
      runtimeError: false,
    });
  });

  it("marks a suite that produced no assertions as a runtime error", () => {
    expect(suiteResultFromJest({ testResults: [] })).toEqual({
      passed: [],
      failed: [],
      flakey: [],
      runtimeError: true,
    });
  });
});

describe("buildBaselineManifest", () => {
  it("keys each suite's outcome by its test file", () => {
    const manifest = buildBaselineManifest([
      {
        path: "test/e2e/a.test.ts.results.json",
        results: { testResults: [{ assertionResults: [{ title: "ok", status: "passed" }] }] },
      },
    ]);
    expect(manifest).toEqual({
      "test/e2e/a.test.ts": { passed: ["ok"], failed: [], flakey: [], runtimeError: false },
    });
  });
});

describe("mergeBaselineManifest", () => {
  it("unions each suite's passed and failed cases across groups", () => {
    const merged = mergeBaselineManifest([
      { "test/a.test.ts": { passed: ["one"], failed: ["two"], flakey: [], runtimeError: false } },
      { "test/a.test.ts": { passed: ["three"], failed: ["two"], flakey: [], runtimeError: false } },
      { "test/b.test.ts": { passed: [], failed: [], flakey: [], runtimeError: true } },
    ]);
    expect(merged).toEqual({
      "test/a.test.ts": { passed: ["one", "three"], failed: ["two"], flakey: [], runtimeError: false },
      "test/b.test.ts": { passed: [], failed: [], flakey: [], runtimeError: true },
    });
  });

  it("keeps a suite that any group saw crash marked as a runtime error", () => {
    const merged = mergeBaselineManifest([
      { "test/a.test.ts": { passed: ["one"], failed: [], flakey: [], runtimeError: false } },
      { "test/a.test.ts": { passed: [], failed: [], flakey: [], runtimeError: true } },
    ]);
    expect(merged["test/a.test.ts"].runtimeError).toBe(true);
  });

  it("sorts suites so a committed baseline has a stable diff", () => {
    const merged = mergeBaselineManifest([
      { "test/b.test.ts": { passed: [], failed: [], flakey: [], runtimeError: false } },
      { "test/a.test.ts": { passed: [], failed: [], flakey: [], runtimeError: false } },
    ]);
    expect(Object.keys(merged)).toEqual(["test/a.test.ts", "test/b.test.ts"]);
  });
});
