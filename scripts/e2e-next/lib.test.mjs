import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";

import {
  APP_NAME,
  BYTECODE_EMBEDDED_MARKER,
  BYTECODE_EMBED_ENV,
  BYTECODE_S3_REHYDRATE_MARKER,
  DNS_LABEL,
  GOLDEN_MARKER,
  GOLDEN_REVALIDATE_SECONDS,
  GOLDEN_ROUTE,
  ISR_REVALIDATE_SECONDS,
  ISR_ROUTE,
  MAX_SLUG_LEN,
  WARM_SUMMARY_MARKER,
  appAssetPrefix,
  buildBaselineManifest,
  bytecodeAppNamespace,
  bytecodeCacheEntry,
  bytecodeCacheKeyName,
  bytecodeEmbedEnabled,
  bytecodeEmbeddedOutcome,
  bytecodeRehydrateOutcome,
  deployURL,
  embeddedArtifactPairs,
  embeddedBytecodePath,
  envSegment,
  goldenDifferences,
  isrToken,
  lambdaFunctionNames,
  lambdaLogGroups,
  logWindowVerdict,
  markerLines,
  mergeBaselineManifest,
  projectSlug,
  projectSlugForApp,
  renderOcelConfig,
  strongestCoverage,
  suiteFromResultsPath,
  summarizeOutcomes,
  suiteResultFromJest,
  tail,
  tarEntryNames,
  warmCoverage,
  warmSummaryOutcome,
  withBuildScript,
  zipEntryNames,
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

describe("bytecodeAppNamespace", () => {
  it("joins bytecode/env/slug/app in that order — mirrors bytecodeAppNamespace in bytecode.go", () => {
    expect(
      bytecodeAppNamespace({ environment: { class: "preview", identity: "e2e-42-abcd1234" }, slug: "e2e-42-abcd1234", app: "app" }),
    ).toBe("bytecode/preview-e2e-42-abcd1234/e2e-42-abcd1234/app");
  });

  it("uses the fixed prod segment for a production deploy", () => {
    expect(bytecodeAppNamespace({ environment: { class: "production" }, slug: "s", app: "app" })).toBe(
      "bytecode/prod/s/app",
    );
  });

  it("is not appAssetPrefix's build-keyed prefix — the two must never collide", () => {
    const environment = { class: "preview", identity: "e2e-42-abcd1234" };
    const slug = "e2e-42-abcd1234";
    const app = "app";
    expect(bytecodeAppNamespace({ environment, slug, app })).not.toBe(
      appAssetPrefix({ environment, slug, app, buildId: "bld123" }),
    );
  });
});

describe("bytecodeCacheEntry", () => {
  // Pins the full derivation this harness has to agree with the Go side on,
  // end to end: bytecodeAppNamespace + bytecodePrefixFor's hash segment +
  // bytecodeCacheKey's own "node<version>-<arch>.tar.gz" (cloud/aws/deploy/
  // bytecode.go, cloud/aws/cmd/lambdanode/bootstrap/bytecode.go). A real S3
  // listing under bytecodeAppNamespace returns names with the namespace
  // already stripped, which is what this parses. Carries no function name:
  // the key is keyed off content hash alone, so two functions whose `.func`
  // trees hash identically share one cache object.
  it("reads the hash, node version and arch out of a real key's tail", () => {
    const namespace = bytecodeAppNamespace({
      environment: { class: "preview", identity: "e2e-42-abcd1234" },
      slug: "e2e-42-abcd1234",
      app: "app",
    });
    const hash = "a1b2c3d4e5f6";
    const key = `${namespace}/${hash}/node24.3.1-x86_64.tar.gz`;

    expect(bytecodeCacheEntry(key.slice(namespace.length + 1))).toEqual({
      hash,
      filename: "node24.3.1-x86_64.tar.gz",
      nodeVersion: "24.3.1",
      arch: "x86_64",
    });
  });

  it("rejects a name with no hash/filename structure", () => {
    expect(bytecodeCacheEntry("node24.3.1-x86_64.tar.gz")).toBeNull();
  });

  it("rejects a name whose filename is not node<version>-<arch>.tar.gz", () => {
    expect(bytecodeCacheEntry("a1b2c3/not-a-cache-file")).toBeNull();
  });
});

describe("bytecodeCacheKeyName", () => {
  it("parses a real archive name into its node version and arch", () => {
    expect(bytecodeCacheKeyName("node24.3.1-x86_64.tar.gz")).toEqual({ nodeVersion: "24.3.1", arch: "x86_64" });
  });

  it("rejects a version that is not three dot-separated numbers", () => {
    expect(bytecodeCacheKeyName("node24-x86_64.tar.gz")).toBeNull();
    expect(bytecodeCacheKeyName("nodev24.3.1-x86_64.tar.gz")).toBeNull();
  });

  it("rejects anything that is not the node<version>-<arch>.tar.gz shape", () => {
    expect(bytecodeCacheKeyName("some-other-object.tar.gz")).toBeNull();
    expect(bytecodeCacheKeyName("")).toBeNull();
    expect(bytecodeCacheKeyName(undefined)).toBeNull();
  });
});

describe("bytecodeRehydrateOutcome", () => {
  const key = "preview-e2e-42/e2e-42/app/bld123/bytecode/proj--web-abc123/node24.3.1-x86_64.tar.gz";

  it("recognizes a rehydrate hit naming the key", () => {
    expect(bytecodeRehydrateOutcome(`ocel: rehydrated compile cache from ${key}: 4096 bytes in 312ms`, key)).toEqual({
      kind: "hit",
      message: `ocel: rehydrated compile cache from ${key}: 4096 bytes in 312ms`,
    });
  });

  it("recognizes the expected first-cold-start miss", () => {
    expect(bytecodeRehydrateOutcome(`ocel: no compile cache at ${key} yet; nothing to rehydrate`, key).kind).toBe(
      "miss",
    );
  });

  it("recognizes a fetch failure", () => {
    expect(
      bytecodeRehydrateOutcome(`ocel: could not fetch the compile cache at ${key}: connection reset`, key).kind,
    ).toBe("fetch-error");
  });

  it("ignores unrelated log lines, and a line naming a different key", () => {
    expect(bytecodeRehydrateOutcome("START RequestId: abc", key)).toBeNull();
    expect(bytecodeRehydrateOutcome(`ocel: rehydrated compile cache from some/other/key.tar.gz: 10 bytes in 1ms`, key)).toBeNull();
  });

  it("recognizes a cache over the ceiling", () => {
    expect(
      bytecodeRehydrateOutcome(
        `ocel: compile cache at ${key} is 100000000 bytes, over the 67108864 byte ceiling; skipping rehydration`,
        key,
      ).kind,
    ).toBe("over-ceiling");
  });

  it("recognizes an extraction failure, and does not confuse it with a hit", () => {
    const message = `ocel: could not rehydrate the compile cache from ${key}: unexpected EOF`;
    expect(bytecodeRehydrateOutcome(message, key)).toEqual({ kind: "extract-error", message });
    // "could not rehydrate the compile cache from" vs "rehydrated compile
    // cache from" — one wrong word here would fold a real failure into a hit.
    expect(bytecodeRehydrateOutcome(message, key).kind).not.toBe("hit");
  });

  it("recognizes a rehydration timeout", () => {
    expect(
      bytecodeRehydrateOutcome(`ocel: rehydrating the compile cache from ${key} ran out of time: context deadline exceeded`, key)
        .kind,
    ).toBe("timeout");
  });

  it("recognizes a dir-clear failure that names no key at all", () => {
    expect(
      bytecodeRehydrateOutcome("ocel: could not clear /tmp/.ocel/compile-cache before rehydrating the compile cache: permission denied", key)
        .kind,
    ).toBe("clear-error");
  });

  it("recognizes the feature disabling itself before a key could ever be composed", () => {
    expect(bytecodeRehydrateOutcome("ocel: could not read node's version, compile cache disabled: exit status 1", key).kind).toBe(
      "disabled",
    );
    expect(bytecodeRehydrateOutcome('ocel: not a node version: "garbage", compile cache disabled', key).kind).toBe("disabled");
    expect(bytecodeRehydrateOutcome("ocel: no aws config for the compile cache: no EC2 IMDS role found", key).kind).toBe(
      "disabled",
    );
  });
});

describe("bytecodeEmbedEnabled", () => {
  it("is on for exactly the literal 1, mirroring the deploy's own gate", () => {
    expect(bytecodeEmbedEnabled({ [BYTECODE_EMBED_ENV]: "1" })).toBe(true);
  });

  it("is off when unset or set to anything else", () => {
    expect(bytecodeEmbedEnabled({})).toBe(false);
    expect(bytecodeEmbedEnabled(undefined)).toBe(false);
    expect(bytecodeEmbedEnabled({ [BYTECODE_EMBED_ENV]: "0" })).toBe(false);
    expect(bytecodeEmbedEnabled({ [BYTECODE_EMBED_ENV]: "true" })).toBe(false);
    expect(bytecodeEmbedEnabled({ [BYTECODE_EMBED_ENV]: "" })).toBe(false);
  });
});

describe("embeddedBytecodePath", () => {
  it("mirrors the key's basename into the artifact, minus the gzip layer", () => {
    expect(
      embeddedBytecodePath("preview-e2e-42/e2e-42/app/bld123/bytecode/proj--web-abc123/node24.3.1-arm64.tar.gz"),
    ).toBe(".ocel/bytecode/node24.3.1-arm64.tar");
    expect(embeddedBytecodePath("node24.3.1-x86_64.tar.gz")).toBe(".ocel/bytecode/node24.3.1-x86_64.tar");
  });

  it("refuses a key that names no cache tarball", () => {
    expect(embeddedBytecodePath("a/b/some-other-object.tar.gz")).toBeNull();
    expect(embeddedBytecodePath("a/b/node24.3.1-x86_64.tar")).toBeNull();
    expect(embeddedBytecodePath("")).toBeNull();
    expect(embeddedBytecodePath(undefined)).toBeNull();
  });
});

describe("bytecodeEmbeddedOutcome", () => {
  const tarPath = "/var/task/.ocel/bytecode/node24.3.1-x86_64.tar";

  it("recognizes a hit naming the tar", () => {
    const message = `ocel: loaded embedded compile cache from ${tarPath}: 4096 bytes in 7ms`;
    expect(bytecodeEmbeddedOutcome(message, tarPath)).toEqual({ kind: "hit", message });
  });

  it("classifies each way the local leg can fail without calling any of them a hit", () => {
    expect(
      bytecodeEmbeddedOutcome(`ocel: could not open the embedded compile cache at ${tarPath}: permission denied`, tarPath),
    ).toEqual({ kind: "open-error", message: expect.any(String) });
    expect(
      bytecodeEmbeddedOutcome(
        `ocel: could not clear /tmp/.ocel/compile-cache before loading the embedded compile cache at ${tarPath}: read-only`,
        tarPath,
      ).kind,
    ).toBe("clear-error");
    // "could not load the embedded compile cache at" vs "loaded embedded
    // compile cache from" — one wrong word here folds a fall-through into a hit.
    expect(
      bytecodeEmbeddedOutcome(`ocel: could not load the embedded compile cache at ${tarPath}: unexpected EOF`, tarPath).kind,
    ).toBe("load-error");
  });

  it("ignores unrelated lines, and a line naming a different tar", () => {
    expect(bytecodeEmbeddedOutcome("START RequestId: abc", tarPath)).toBeNull();
    expect(
      bytecodeEmbeddedOutcome("ocel: loaded embedded compile cache from /var/task/.ocel/bytecode/node22.0.0-x86_64.tar: 1 bytes in 1ms", tarPath),
    ).toBeNull();
  });

  it("does not confuse the two read legs in either direction", () => {
    const key = "preview/app/bld/bytecode/fn/node24.3.1-x86_64.tar.gz";
    const s3Hit = `ocel: rehydrated compile cache from ${key}: 4096 bytes in 312ms`;
    const embeddedHit = `ocel: loaded embedded compile cache from ${tarPath}: 4096 bytes in 7ms`;
    expect(bytecodeEmbeddedOutcome(s3Hit, tarPath)).toBeNull();
    expect(bytecodeRehydrateOutcome(embeddedHit, key)).toBeNull();
    // The whole CloudWatch attribution rests on this: a caller looks for one
    // marker to require and the other to forbid, so either containing the other
    // would make "read from the artifact" indistinguishable from "read from S3".
    expect(BYTECODE_EMBEDDED_MARKER).not.toContain(BYTECODE_S3_REHYDRATE_MARKER);
    expect(BYTECODE_S3_REHYDRATE_MARKER).not.toContain(BYTECODE_EMBEDDED_MARKER);
    expect(embeddedHit).not.toContain(BYTECODE_S3_REHYDRATE_MARKER);
    expect(s3Hit).not.toContain(BYTECODE_EMBEDDED_MARKER);
  });
});

describe("embeddedArtifactPairs", () => {
  const original = "e2e-42/web-server/abc123.zip";
  const embedded = "e2e-42/web-server/abc123-bc-deadbeefdeadb.zip";

  it("pairs a repackaged artifact with the original it extends", () => {
    expect(embeddedArtifactPairs([original, embedded])).toEqual([
      { embedded, original, digest: "deadbeefdeadb" },
    ]);
  });

  it("reports a repackaged artifact whose original is gone rather than dropping it", () => {
    expect(embeddedArtifactPairs([embedded])).toEqual([{ embedded, original: null, digest: "deadbeefdeadb" }]);
  });

  it("finds none when the deploy embedded nothing", () => {
    expect(embeddedArtifactPairs([original, "e2e-42/other/def456.zip"])).toEqual([]);
    expect(embeddedArtifactPairs([])).toEqual([]);
    expect(embeddedArtifactPairs(undefined)).toEqual([]);
  });

  it("ignores keys that merely look like one", () => {
    expect(embeddedArtifactPairs(["e2e-42/web-server/abc123-bc-nothex.zip"])).toEqual([]);
    expect(embeddedArtifactPairs(["e2e-42/web-server/abc123-bc-deadbeef.tar"])).toEqual([]);
  });
});

describe("warmSummaryOutcome", () => {
  const summary = { state: "published", entries: 12, loaded: 12, stoppedBy: "complete", bytes: 4096, key: "k", uploaded: true };

  it("reads the summary out of the membrane's stderr line", () => {
    const message = `${WARM_SUMMARY_MARKER} ${JSON.stringify(summary)}`;
    expect(warmSummaryOutcome(message)).toEqual({ kind: "summary", summary, message });
  });

  it("ignores every other log line", () => {
    expect(warmSummaryOutcome("START RequestId: abc")).toBeNull();
    expect(warmSummaryOutcome("ocel: rehydrated compile cache from k: 10 bytes in 1ms")).toBeNull();
    expect(warmSummaryOutcome(undefined)).toBeNull();
  });

  it("reports a marked line whose JSON was truncated rather than dropping it", () => {
    const outcome = warmSummaryOutcome(`${WARM_SUMMARY_MARKER} {"state":"publis`);
    expect(outcome.kind).toBe("unreadable");
    expect(outcome.reason).toBeTruthy();
  });
});

describe("warmCoverage", () => {
  const key = "preview-e2e-42/e2e-42/app/bld123/bytecode/proj--web-abc123/node24.3.1-x86_64.tar.gz";
  const published = { state: "published", entries: 12, loaded: 12, stoppedBy: "complete", bytes: 4096, key, uploaded: true };

  it("proves a whole bundle when this pass's own PUT created the object", () => {
    expect(warmCoverage(published, key).kind).toBe("complete");
  });

  it("reports a walk the ceiling or the deadline cut short as partial", () => {
    expect(warmCoverage({ ...published, loaded: 7, stoppedBy: "ceiling" }, key).kind).toBe("partial");
    expect(warmCoverage({ ...published, loaded: 7, stoppedBy: "deadline" }, key).kind).toBe("partial");
  });

  it("reports entries that failed to load as partial even on a complete walk", () => {
    expect(warmCoverage({ ...published, loaded: 11, failures: [{ entry: "/x", message: "boom" }] }, key).kind).toBe(
      "partial",
    );
  });

  it("refuses to believe a published state that claims no upload", () => {
    expect(warmCoverage({ ...published, uploaded: false }, key).kind).toBe("failed");
    expect(warmCoverage({ ...published, uploaded: undefined }, key).kind).toBe("failed");
  });

  it("refuses to call a bundle with no entries at all covered", () => {
    expect(warmCoverage({ ...published, entries: 0, loaded: 0 }, key).kind).toBe("failed");
  });

  // The membrane publishes what INIT loaded even when node never reported back,
  // so an uncounted publish is a real object with unknown coverage — not the
  // "no entries at all" contradiction it would otherwise be mistaken for.
  it("reports a publish nobody could account for as partial, with the reason", () => {
    const verdict = warmCoverage(
      { state: "published", uploaded: true, bytes: 4096, key, uncounted: "node did not report back" },
      key,
    );
    expect(verdict.kind).toBe("partial");
    expect(verdict.detail).toContain("node did not report back");
  });

  it("names the entries a stopped walk skipped", () => {
    const verdict = warmCoverage(
      { ...published, loaded: 7, stoppedBy: "ceiling", skippedCount: 5, skipped: ["app/a/page"] },
      key,
    );
    expect(verdict.kind).toBe("partial");
    expect(verdict.detail).toContain("5 skipped");
    expect(verdict.detail).toContain("app/a/page");
  });

  it("proves nothing from an already-cached pass, whether or not it walked", () => {
    expect(warmCoverage({ state: "already-cached" }, key).kind).toBe("unproven");
    expect(warmCoverage({ ...published, state: "already-cached", uploaded: undefined }, key).kind).toBe("unproven");
  });

  it("treats disabled, failed and an unknown state as failures", () => {
    expect(warmCoverage({ state: "disabled", error: "no capability" }, key).kind).toBe("failed");
    expect(warmCoverage({ state: "failed", entries: 12, loaded: 3, error: "put denied" }, key).kind).toBe("failed");
    expect(warmCoverage({ state: "somethingelse" }, key).kind).toBe("failed");
  });

  it("sets a summary naming another build's key aside", () => {
    expect(warmCoverage({ ...published, key: "other/build/node24.3.1-x86_64.tar.gz" }, key).kind).toBe("other-build");
  });
});

describe("strongestCoverage", () => {
  it("takes the pass that actually wrote the object over one that found it there", () => {
    const complete = { kind: "complete", detail: "" };
    expect(strongestCoverage([{ kind: "unproven", detail: "" }, complete])).toBe(complete);
    expect(strongestCoverage([complete, { kind: "failed", detail: "" }])).toBe(complete);
  });

  it("prefers a partial pass to none at all, and reports nothing seen as null", () => {
    const partial = { kind: "partial", detail: "" };
    expect(strongestCoverage([{ kind: "failed", detail: "" }, partial])).toBe(partial);
    expect(strongestCoverage([])).toBeNull();
  });
});

describe("summarizeOutcomes", () => {
  it("counts outcomes by kind", () => {
    expect(
      summarizeOutcomes([{ kind: "miss" }, { kind: "fetch-error" }, { kind: "miss" }]),
    ).toBe("2 miss, 1 fetch-error");
  });

  it("says so when nothing related was seen at all", () => {
    expect(summarizeOutcomes([])).toBe("0 related lines");
  });
});

describe("logWindowVerdict", () => {
  const pageLimit = 1000;

  it("reads a window that was polled cleanly to its end", () => {
    const verdict = logWindowVerdict({ attempts: 5, failures: 0, confirmed: true, events: 12, pageLimit });
    expect(verdict.kind).toBe("read");
    expect(verdict.detail).toContain("5/5");
  });

  it("still reads it when polls failed but the last read succeeded", () => {
    // The window is re-read from its start every time, so one successful read
    // at the end covers everything the failed ones missed.
    expect(logWindowVerdict({ attempts: 5, failures: 4, confirmed: true, events: 12, pageLimit }).kind).toBe("read");
  });

  it("reports a window never read to its end as unread, however many polls succeeded", () => {
    const verdict = logWindowVerdict({ attempts: 13, failures: 1, confirmed: false, events: 0, pageLimit });
    expect(verdict.kind).toBe("unread");
    expect(verdict.detail).toContain("12/13");
  });

  it("reports a full page as truncated, because a line could have been dropped off its end", () => {
    const verdict = logWindowVerdict({ attempts: 2, failures: 0, confirmed: true, events: pageLimit, pageLimit });
    expect(verdict.kind).toBe("truncated");
    expect(verdict.detail).toContain(String(pageLimit));
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

describe("zipEntryNames", () => {
  it("reads every entry's name out of the central directory", () => {
    const zip = buildZip([
      { name: "index.mjs", content: "export default 1" },
      { name: ".ocel/variables.enc", content: "cipher" },
      { name: ".ocel/bytecode/node24.3.1-x86_64.tar", content: "tar bytes" },
    ]);
    expect(zipEntryNames(zip)).toEqual([
      "index.mjs",
      ".ocel/variables.enc",
      ".ocel/bytecode/node24.3.1-x86_64.tar",
    ]);
  });

  it("reads an empty zip", () => {
    expect(zipEntryNames(buildZip([]))).toEqual([]);
  });

  it("finds the record past a trailing comment", () => {
    const zip = buildZip([{ name: "a.txt", content: "a" }], "a comment the scan has to walk back over");
    expect(zipEntryNames(zip)).toEqual(["a.txt"]);
  });

  it("takes the last end-of-central-directory record, not one an entry's bytes spell", () => {
    const decoy = Buffer.alloc(22);
    decoy.writeUInt32LE(0x06054b50, 0);
    const zip = buildZip([{ name: "decoy.bin", content: decoy.toString("latin1") }]);
    expect(zipEntryNames(zip)).toEqual(["decoy.bin"]);
  });

  it("throws rather than returning a short list for anything malformed", () => {
    expect(() => zipEntryNames(Buffer.from("not a zip at all"))).toThrow(/not a zip/);
    const zip = buildZip([{ name: "a.txt", content: "a" }]);
    // Blow away the central-file-header signature the EOCD still points at.
    const corrupt = Buffer.from(zip);
    corrupt.writeUInt32LE(0, corrupt.readUInt32LE(corrupt.length - 6));
    expect(() => zipEntryNames(corrupt)).toThrow(/no central-file-header signature/);
  });

  it("throws when the central directory is truncated away", () => {
    const zip = buildZip([{ name: "a.txt", content: "a" }]);
    const eocd = Buffer.from(zip.subarray(zip.length - 22));
    // An EOCD claiming a directory that starts past the end of the buffer.
    eocd.writeUInt32LE(0xfffffff0, 16);
    expect(() => zipEntryNames(eocd)).toThrow(/runs past the end of the buffer/);
  });
});

// buildZip is a minimal stored-mode zip writer used only to build fixtures for
// zipEntryNames: real packages always come from Go's archive/zip. CRCs are left
// zero because nothing under test reads them.
function buildZip(entries, comment = "") {
  const locals = [];
  const centrals = [];
  let offset = 0;
  for (const entry of entries) {
    const name = Buffer.from(entry.name, "utf8");
    const data = Buffer.from(entry.content, "latin1");
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    locals.push(local, name, data);

    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 6);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt32LE(offset, 42);
    centrals.push(central, name);
    offset += 30 + name.length + data.length;
  }

  const directory = Buffer.concat(centrals);
  const commentBytes = Buffer.from(comment, "utf8");
  const eocd = Buffer.alloc(22);
  eocd.writeUInt32LE(0x06054b50, 0);
  eocd.writeUInt16LE(entries.length, 8);
  eocd.writeUInt16LE(entries.length, 10);
  eocd.writeUInt32LE(directory.length, 12);
  eocd.writeUInt32LE(offset, 16);
  eocd.writeUInt16LE(commentBytes.length, 20);
  return Buffer.concat([...locals, directory, eocd, commentBytes]);
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
