import { describe, expect, it, vi } from "vitest";

import {
  APP_NAME,
  DNS_LABEL,
  ECHO_MARKER,
  ECHO_ROUTE,
  FRAMEWORK,
  HEALTH_ROUTE,
  MAX_SLUG_LEN,
  WARM_SUMMARY_MARKER,
  bytecodeRehydrateOutcome,
  echoValue,
  projectSlug,
  projectSlugForApp,
  renderOcelConfig,
  warmCoverage,
  warmSummaryOutcome,
} from "./lib.mjs";
import { lambdaRegion } from "./sigv4.mjs";

// This harness's own additions to @ocel-scripts/e2e-shared: how its project
// slug is derived without an external test harness driving it, its
// ocel.config.ts framework declaration, its smoke app's own routes/markers,
// and the SigV4 region parsing scripts/e2e-next never needs (see
// sigv4.mjs's own header for why). Everything re-exported from
// @ocel-scripts/e2e-shared/lib.mjs is exercised transitively through
// scripts/e2e-next/lib.test.mjs, which imports the identical functions from
// its own lib.mjs; a few are spot-checked again here against this harness's
// own shape of inputs (a report-only node warm, an AWS_IAM Function URL host)
// so this package's test run means something on its own.

describe("projectSlugForApp", () => {
  it("derives the slug straight from the app directory — no external harness hands this one a temp dir", () => {
    vi.stubEnv("GITHUB_RUN_ID", "42");
    expect(projectSlugForApp("/tmp/ocel-e2e-node-abc123")).toBe(
      projectSlug({ runId: "42", dir: "/tmp/ocel-e2e-node-abc123" }),
    );
  });

  it("gives two directories their own project", () => {
    vi.stubEnv("GITHUB_RUN_ID", "42");
    expect(projectSlugForApp("/tmp/a")).not.toBe(projectSlugForApp("/tmp/b"));
  });

  it("derives the same slug twice, so cleanup can recover it without the state file", () => {
    vi.stubEnv("GITHUB_RUN_ID", "42");
    expect(projectSlugForApp("/tmp/ocel-e2e-node-abc123")).toBe(projectSlugForApp("/tmp/ocel-e2e-node-abc123"));
  });

  it("is a valid single DNS label within the slug budget", () => {
    vi.stubEnv("GITHUB_RUN_ID", "9".repeat(200));
    const slug = projectSlugForApp("/tmp/ocel-e2e-node-abc123");
    expect(slug).toMatch(DNS_LABEL);
    expect(slug.length).toBeLessThanOrEqual(MAX_SLUG_LEN);
  });
});

describe("renderOcelConfig", () => {
  it("declares the smoke app under the constant name and the express framework", () => {
    expect(APP_NAME).toBe("app");
    expect(FRAMEWORK).toBe("express");
    const config = renderOcelConfig({ slug: "e2e-42-abcd1234" });
    expect(config).toContain(`apps: [{ name: "app", path: ".", framework: "express" }]`);
    expect(config).toContain(`slug: "e2e-42-abcd1234"`);
    expect(config).toContain("awsProvider()");
  });

  it("is pure, so cleanup.mjs re-renders byte-for-byte what deploy.mjs wrote", () => {
    const args = { slug: projectSlugForApp("/tmp/ocel-e2e-node-abc123"), previewDomain: "" };
    expect(renderOcelConfig(args)).toBe(renderOcelConfig(args));
  });

  it("omits the domains block when no preview domain is configured — a plain node app registers no worker to route through anyway", () => {
    expect(renderOcelConfig({ slug: "s" })).not.toContain("domains");
  });
});

describe("echoValue", () => {
  it("names one run's probe uniquely, and carries ECHO_MARKER separately for the response body", () => {
    expect(ECHO_ROUTE).toBe("/echo");
    expect(ECHO_MARKER).toBe("ocel-e2e-node-smoke-v1");
    expect(echoValue("stamp-1")).toBe("ocel-e2e-node-echo-stamp-1");
    expect(echoValue("stamp-1")).not.toBe(echoValue("stamp-2"));
  });
});

describe("HEALTH_ROUTE", () => {
  it("is the readiness probe smoke-app/src/server.js declares", () => {
    expect(HEALTH_ROUTE).toBe("/health");
  });
});

describe("lambdaRegion", () => {
  it("extracts the region from a Function URL host", () => {
    expect(lambdaRegion("abc123def.lambda-url.us-east-1.on.aws")).toBe("us-east-1");
  });

  it("is undefined for a host that is not a Function URL", () => {
    expect(lambdaRegion("example.com")).toBeUndefined();
    expect(lambdaRegion("abc123def.on.aws")).toBeUndefined();
  });
});

describe("warmSummaryOutcome + warmCoverage: a node app's report-only warm", () => {
  it("reports entries:1, loaded:1 for the one unit loadUserApp already loaded at INIT — not uncounted", () => {
    // warmNode (packages/lambda-entrypoints/src/node/entrypoint.mts) always
    // answers entries:1/loaded:1/stoppedBy:"complete" once it can flush a
    // compile cache directory at all: a node app has no entry table to walk,
    // so this is the whole story rather than a partial one.
    const message =
      `${WARM_SUMMARY_MARKER} ` +
      JSON.stringify({
        state: "published",
        entries: 1,
        loaded: 1,
        stoppedBy: "complete",
        bytes: 4096,
        key: "bytecode/preview-x/slug/app/deadbeef/bytecode/fn/node22.1.0-x86_64.tar.gz",
        source: "none",
        uploaded: true,
      });
    const outcome = warmSummaryOutcome(message);
    expect(outcome.kind).toBe("summary");
    const verdict = warmCoverage(outcome.summary, outcome.summary.key);
    expect(verdict.kind).toBe("complete");
  });

  it("classifies the pre-warm-report line the assertion must never see again", () => {
    // cloud/aws/cmd/lambdanode/bootstrap/warm.go's warmSummary.count sets this
    // exact string when node's control-socket reply never arrived — the
    // failure mode a patched entrypoint (which now always answers) exists to
    // eliminate for a node app specifically, since it has nothing else to
    // report from.
    const message =
      `${WARM_SUMMARY_MARKER} ` +
      JSON.stringify({
        state: "published",
        uncounted: "node did not report back on the compile-cache warm",
        bytes: 2048,
        key: "bytecode/preview-x/slug/app/deadbeef/bytecode/fn/node22.1.0-x86_64.tar.gz",
        source: "none",
        uploaded: true,
      });
    const outcome = warmSummaryOutcome(message);
    expect(outcome.summary.uncounted).toContain("did not report back");
    // Still a real, attributed publish — assert-bytecode.mjs treats this as
    // "partial" coverage and fails on the uncounted line separately, rather
    // than folding it into the coverage verdict itself.
    const verdict = warmCoverage(outcome.summary, outcome.summary.key);
    expect(verdict.kind).toBe("partial");
  });
});

describe("bytecodeRehydrateOutcome: log matching against this harness's own key shape", () => {
  it("matches a rehydrate hit naming a node-app bytecode key", () => {
    const key = "bytecode/preview-x/slug/app/deadbeef/bytecode/fn/node22.1.0-x86_64.tar.gz";
    const outcome = bytecodeRehydrateOutcome(`rehydrated compile cache from ${key}: 12ms`, key);
    expect(outcome).toEqual({ kind: "hit", message: `rehydrated compile cache from ${key}: 12ms` });
  });

  it("is null for an unrelated line", () => {
    expect(bytecodeRehydrateOutcome("e2e-node smoke app listening on http://localhost:3000", "irrelevant-key")).toBeNull();
  });
});
