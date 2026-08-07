import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";

import {
  APP_NAME,
  DNS_LABEL,
  GOLDEN_MARKER,
  GOLDEN_REVALIDATE_SECONDS,
  GOLDEN_ROUTE,
  ISR_REVALIDATE_SECONDS,
  ISR_ROUTE,
  LAMBDA_RUNTIME,
  MAX_SLUG_LEN,
  appAssetPrefix,
  buildBaselineManifest,
  bytecodeCacheKey,
  deployURL,
  envSegment,
  goldenDifferences,
  isrToken,
  lambdaFunctionNames,
  lambdaLogGroups,
  markerLines,
  mergeBaselineManifest,
  nodeMajorFromRuntime,
  projectSlug,
  projectSlugForApp,
  renderOcelConfig,
  suiteFromResultsPath,
  suiteResultFromJest,
  tail,
  tarEntryNames,
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

describe("goldenDifferences", () => {
  const leg = (over = {}) => ({
    status: 200,
    headers: { "content-type": "text/html; charset=utf-8", etag: '"abc"' },
    body: `<p id="golden-body">${GOLDEN_MARKER}</p>`,
    ...over,
  });

  it("finds nothing when the header changed nothing", () => {
    expect(goldenDifferences(leg(), leg())).toEqual([]);
  });

  it("ignores the headers that differ between any two responses", () => {
    const withHeader = leg({
      headers: { ...leg().headers, date: "Mon, 01 Jan 2035 00:00:00 GMT", "x-nextjs-cache": "STALE", "x-ocel-cache": "BYPASS" },
    });
    const without = leg({
      headers: { ...leg().headers, date: "Mon, 01 Jan 2035 00:00:07 GMT", "x-nextjs-cache": "HIT", "x-ocel-cache": "MISS" },
    });

    expect(goldenDifferences(withHeader, without)).toEqual([]);
  });

  // The caveat this gate exists for: OpenNext's, now ours — a future Next could
  // make `purpose` change what is rendered rather than only whether a
  // revalidation is started. A shell where a full page was is what that looks
  // like.
  it("reports a body the header changed, and where", () => {
    const [difference, ...rest] = goldenDifferences(leg({ body: "<p>shell</p>" }), leg());

    expect(rest).toEqual([]);
    expect(difference).toContain("body:");
    expect(difference).toContain("lengths");
  });

  it("reports a body of the same length that diverges mid-string", () => {
    expect(goldenDifferences(leg({ body: "abcdef" }), leg({ body: "abcXef" }))[0]).toContain(
      "differs at offset 3",
    );
  });

  it("reports a differing status", () => {
    expect(goldenDifferences(leg({ status: 404 }), leg())[0]).toBe("status: with=404 without=200");
  });

  it("reports a header present on one leg only, and one whose value changed", () => {
    const withHeader = leg({
      headers: { ...leg().headers, etag: '"zzz"', "x-nextjs-postponed": "1" },
    });

    expect(goldenDifferences(withHeader, leg())).toEqual([
      'header etag: with="zzz" without="abc"',
      "header x-nextjs-postponed: with=1 without=(absent)",
    ]);
  });

  it("reads a real Headers object, which is what the live assertion passes", () => {
    const headers = new Headers({ "content-type": "text/html; charset=utf-8", etag: '"abc"' });

    expect(goldenDifferences(leg({ headers }), leg())).toEqual([]);
  });

  it("matches the marker the smoke app's probe page emits", () => {
    const page = readFileSync(new URL("./smoke-app/app/golden/page.tsx", import.meta.url), "utf8");
    expect(page).toContain(GOLDEN_MARKER);
    expect(GOLDEN_ROUTE).toBe("/golden");
    // Nothing per-render, or the comparison can never be byte-exact.
    expect(page).not.toContain("Date.now");
    expect(page).not.toContain("Math.random");
  });

  // The assertion waits GOLDEN_REVALIDATE_SECONDS out to get both legs answered
  // from a stale entry. A page whose own window is longer than that number puts
  // the comparison back on a fresh entry, where the operand under test is
  // short-circuited and the gate silently proves nothing.
  it("waits out the window the probe page actually declares", () => {
    const page = readFileSync(new URL("./smoke-app/app/golden/page.tsx", import.meta.url), "utf8");

    expect(page).toContain(`export const revalidate = ${GOLDEN_REVALIDATE_SECONDS};`);
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

describe("lambdaFunctionNames", () => {
  it("extracts bare function names from tagged ARNs", () => {
    expect(
      lambdaFunctionNames({
        ResourceTagMappingList: [{ ResourceARN: "arn:aws:lambda:us-east-1:1:function:proj--web-abc123" }],
      }),
    ).toEqual(["proj--web-abc123"]);
  });

  it("agrees with lambdaLogGroups on the function each names", () => {
    const response = {
      ResourceTagMappingList: [
        { ResourceARN: "arn:aws:lambda:us-east-1:1:function:a" },
        { ResourceARN: "arn:aws:lambda:us-east-1:1:function:b" },
      ],
    };
    expect(lambdaLogGroups(response)).toEqual(lambdaFunctionNames(response).map((name) => `/aws/lambda/${name}`));
  });
});

describe("envSegment", () => {
  it("names a preview by its identity", () => {
    expect(envSegment({ class: "preview", identity: "e2e-42-abcd1234" })).toBe("preview-e2e-42-abcd1234");
  });

  it("names production the fixed token, regardless of identity", () => {
    expect(envSegment({ class: "production", identity: "" })).toBe("prod");
    expect(envSegment({ class: "production" })).toBe("prod");
  });

  it("treats a missing or unrecognized class as production", () => {
    expect(envSegment(undefined)).toBe("prod");
    expect(envSegment({ class: "development" })).toBe("prod");
  });
});

describe("appAssetPrefix", () => {
  it("joins env/slug/app/buildId in that order", () => {
    expect(
      appAssetPrefix({
        environment: { class: "preview", identity: "e2e-42-abcd1234" },
        slug: "e2e-42-abcd1234",
        app: "app",
        buildId: "bld123",
      }),
    ).toBe("preview-e2e-42-abcd1234/e2e-42-abcd1234/app/bld123");
  });

  it("uses the fixed prod segment for a production deploy", () => {
    expect(
      appAssetPrefix({ environment: { class: "production" }, slug: "s", app: "app", buildId: "b" }),
    ).toBe("prod/s/app/b");
  });
});

describe("nodeMajorFromRuntime", () => {
  it("reads the major version off the lambda runtime name", () => {
    expect(nodeMajorFromRuntime("nodejs24.x")).toBe(24);
    expect(nodeMajorFromRuntime(LAMBDA_RUNTIME)).toBe(24);
  });

  it("refuses a string that is not a lambda node runtime", () => {
    expect(() => nodeMajorFromRuntime("node24")).toThrow(/not a lambda node runtime/);
    expect(() => nodeMajorFromRuntime(undefined)).toThrow(/not a lambda node runtime/);
  });
});

describe("bytecodeCacheKey", () => {
  it("composes the key the membrane uploads to, matching bytecode.go's format", () => {
    expect(
      bytecodeCacheKey({ prefix: "preview-e2e-42/e2e-42/app/bld123", functionName: "proj--web-abc123", nodeMajor: 24, arch: "x86_64" }),
    ).toBe("preview-e2e-42/e2e-42/app/bld123/bytecode/proj--web-abc123/node24-x86_64.tar.gz");
  });
});

describe("tarEntryNames", () => {
  it("reads the entries of a valid tar, honoring the prefix field", () => {
    const tar = buildTar([
      { name: "top.bin", content: "top" },
      { name: "nested/inner.bin", content: "nested contents" },
    ]);
    expect(tarEntryNames(tar)).toEqual(["top.bin", "nested/inner.bin"]);
  });

  it("returns no entries for an otherwise-valid empty archive", () => {
    expect(tarEntryNames(buildTar([]))).toEqual([]);
  });

  it("rejects a buffer with no end-of-archive marker", () => {
    const tar = buildTar([{ name: "a.bin", content: "a" }]);
    expect(() => tarEntryNames(tar.subarray(0, 512))).toThrow(/no end-of-archive marker/);
  });

  it("rejects a header cut off mid-block", () => {
    const tar = buildTar([{ name: "a.bin", content: "a" }]);
    expect(() => tarEntryNames(tar.subarray(0, 100))).toThrow(/truncated tar header/);
  });

  it("rejects a header whose checksum was corrupted", () => {
    const tar = buildTar([{ name: "a.bin", content: "a" }]);
    tar[0] ^= 0xff;
    expect(() => tarEntryNames(tar)).toThrow(/fails its checksum/);
  });

  it("splits a name longer than the 100-byte field across the ustar prefix", () => {
    const longDir = "d".repeat(120);
    const tar = buildTar([{ name: `${longDir}/f.bin`, content: "x" }]);
    expect(tarEntryNames(tar)).toEqual([`${longDir}/f.bin`]);
  });
});

// buildTar is a minimal, from-scratch ustar writer used only to build fixtures
// for tarEntryNames: real archives always come from Go's archive/tar (via
// gzip, in production) or from tar(1) in a human's shell, never from here.
function buildTar(entries) {
  const blocks = entries.map((entry) => buildTarEntry(entry.name, entry.content));
  blocks.push(Buffer.alloc(1024));
  return Buffer.concat(blocks);
}

function buildTarEntry(name, content) {
  const data = Buffer.from(content, "utf8");
  const header = Buffer.alloc(512);
  let path = name;
  let prefix = "";
  if (path.length > 100) {
    const split = path.lastIndexOf("/", path.length - 1);
    prefix = path.slice(0, split);
    path = path.slice(split + 1);
  }
  header.write(path, 0, 100, "utf8");
  header.write("0000644\0", 100, 8, "utf8");
  header.write("0000000\0", 108, 8, "utf8");
  header.write("0000000\0", 116, 8, "utf8");
  header.write(data.length.toString(8).padStart(11, "0") + "\0", 124, 12, "utf8");
  header.write("00000000000\0", 136, 12, "utf8");
  header.fill(0x20, 148, 156);
  header.write("0", 156, 1, "utf8");
  header.write("ustar\0", 257, 6, "utf8");
  header.write("00", 263, 2, "utf8");
  if (prefix) header.write(prefix, 345, 155, "utf8");

  let sum = 0;
  for (const byte of header) sum += byte;
  header.write(sum.toString(8).padStart(6, "0") + "\0 ", 148, 8, "utf8");

  const dataBlocks = Math.ceil(data.length / 512) * 512;
  const padded = Buffer.alloc(dataBlocks);
  data.copy(padded);
  return Buffer.concat([header, padded]);
}

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
