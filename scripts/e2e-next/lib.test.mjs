import { describe, expect, it } from "vitest";

import {
  DNS_LABEL,
  buildBaselineManifest,
  deployURL,
  lambdaLogGroups,
  markerLines,
  mergeBaselineManifest,
  previewSlug,
  projectSlug,
  renderOcelConfig,
  suiteFromResultsPath,
  suiteResultFromJest,
  tail,
  withBuildScript,
} from "./lib.mjs";

describe("previewSlug", () => {
  it("is a valid single DNS label carrying the run id", () => {
    const slug = previewSlug({ runId: "1234567890", dir: "/tmp/next-e2e-abc" });
    expect(slug).toMatch(DNS_LABEL);
    expect(slug).toContain("1234567890");
    expect(slug.length).toBeLessThanOrEqual(63);
  });

  it("distinguishes two temp apps in the same run", () => {
    const a = previewSlug({ runId: "7", dir: "/tmp/next-e2e-a" });
    const b = previewSlug({ runId: "7", dir: "/tmp/next-e2e-b" });
    expect(a).not.toBe(b);
  });

  it("is stable for the same run id and directory", () => {
    expect(previewSlug({ runId: "7", dir: "/tmp/x" })).toBe(previewSlug({ runId: "7", dir: "/tmp/x" }));
  });

  it("stays a valid label with no run id and a hostile directory name", () => {
    const slug = previewSlug({ runId: "", dir: "/tmp/Next E2E_App/../weird" });
    expect(slug).toMatch(DNS_LABEL);
  });

  it("stays within a DNS label for an absurdly long run id", () => {
    const slug = previewSlug({ runId: "9".repeat(200), dir: "/tmp/x" });
    expect(slug).toMatch(DNS_LABEL);
    expect(slug.length).toBeLessThanOrEqual(63);
  });
});

describe("projectSlug", () => {
  it("lowers a project id into the DNS-label shape a slug must take", () => {
    expect(projectSlug("proj_Ab12")).toBe("proj-ab12");
    expect(projectSlug("adapter-e2e")).toBe("adapter-e2e");
  });

  it("refuses a value with nothing usable in it", () => {
    expect(() => projectSlug("___")).toThrow(/cannot derive/);
  });
});

describe("renderOcelConfig", () => {
  const config = renderOcelConfig({
    slug: "adapter-e2e",
    projectId: "proj_123",
    previewDomain: "*.e2e.example.com",
    appName: "e2e-7-abcd1234",
  });

  it("declares the app explicitly, keyed by the unique slug", () => {
    expect(config).toContain(`{ name: "e2e-7-abcd1234", path: ".", framework: "next" }`);
  });

  it("carries the shared project identity, the provider and the preview wildcard", () => {
    expect(config).toContain(`slug: "adapter-e2e"`);
    expect(config).toContain(`projectId: "proj_123"`);
    expect(config).toContain("awsProvider()");
    expect(config).toContain(`preview: "*.e2e.example.com"`);
  });

  it("omits the domains block when no preview domain is configured", () => {
    expect(renderOcelConfig({ slug: "s", projectId: "p", appName: "a" })).not.toContain("domains");
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

describe("markerLines", () => {
  it("emits the three harness markers in order", () => {
    expect(markerLines({ buildId: "bld", deploymentId: "dep" })).toEqual([
      "BUILD_ID: bld",
      "DEPLOYMENT_ID: dep",
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
