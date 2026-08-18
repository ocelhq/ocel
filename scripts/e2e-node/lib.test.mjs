import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { SLUG_PREFIX as NEXT_SLUG_PREFIX } from "../e2e-next/lib.mjs";
import {
  DNS_LABEL,
  MAX_SLUG_LEN,
  PREVIEW_LABEL_MAX_LEN,
  PREVIEW_ROOT_STACK_PARAM_PREFIX,
  SLUG_PREFIX,
  SMOKE_APPS,
  bytecodeArchiveName,
  bytecodePrefixProblem,
  bytecodeRehydrateOutcome,
  bytecodeSettings,
  echoMismatches,
  echoRequest,
  edgeVerdict,
  envSegment,
  isFunctionURLHost,
  lambdaFunctionNames,
  previewLabel,
  previewLabelProblem,
  previewRef,
  projectSlug,
  renderOcelConfig,
  resolveAppURLs,
  serveMarker,
  strandedProjectSlugs,
  summarizeOutcomes,
  tail,
  urlHost,
} from "./lib.mjs";
import { toolchainArtifacts } from "./toolchain.mjs";

const ZONE = "ocel.site";

const here = new URL(".", import.meta.url).pathname;

function edgeURL(slug, pointer, app) {
  return `https://${previewLabel(slug, pointer, app)}.${ZONE}`;
}

describe("projectSlug", () => {
  it("is a valid single DNS label carrying the run id", () => {
    const slug = projectSlug({ runId: "1234567890" });
    expect(slug).toMatch(DNS_LABEL);
    expect(slug).toContain("1234567890");
    expect(slug.length).toBeLessThanOrEqual(MAX_SLUG_LEN);
  });

  it("gives the whole run one project", () => {
    expect(projectSlug({ runId: "7" })).toBe(projectSlug({ runId: "7" }));
  });

  it("gives two runs their own project", () => {
    expect(projectSlug({ runId: "7" })).not.toBe(projectSlug({ runId: "8" }));
  });

  it("stays a valid label outside CI, where there is no run id", () => {
    expect(projectSlug({ runId: "" })).toMatch(DNS_LABEL);
    expect(projectSlug({ runId: "" })).toBe(`${SLUG_PREFIX}local`);
    expect(projectSlug({})).toBe(projectSlug({ runId: "" }));
  });

  it("stays within the slug budget for an absurdly long run id", () => {
    const slug = projectSlug({ runId: "9".repeat(200) });
    expect(slug).toMatch(DNS_LABEL);
    expect(slug.length).toBeLessThanOrEqual(MAX_SLUG_LEN);
  });

  it("stays a valid label when a hostile run id would cap onto a hyphen", () => {
    const slug = projectSlug({ runId: `${"a".repeat(MAX_SLUG_LEN - SLUG_PREFIX.length - 1)} tail` });
    expect(slug).toMatch(DNS_LABEL);
    expect(slug.length).toBeLessThanOrEqual(MAX_SLUG_LEN);
  });

  it("never collides with the next suite's slugs, so concurrent runs do not fight over one project", () => {
    expect(SLUG_PREFIX.startsWith(NEXT_SLUG_PREFIX)).toBe(false);
    expect(NEXT_SLUG_PREFIX.startsWith(SLUG_PREFIX)).toBe(false);
    expect(projectSlug({ runId: "7" }).startsWith(NEXT_SLUG_PREFIX)).toBe(false);
  });
});

describe("previewRef", () => {
  it("keeps the staged directory's name, so a stranded pointer names the suite that left it", () => {
    expect(previewRef({ dir: "/tmp/node-e2e-abc" })).toMatch(/^node-e2e-a-[0-9a-f]{8}$/);
  });

  it("gives two staged apps their own pointer", () => {
    expect(previewRef({ dir: "/tmp/a" })).not.toBe(previewRef({ dir: "/tmp/b" }));
  });

  it("separates two staged apps whose names collide once truncated", () => {
    const staged = `node-e2e-${"a".repeat(64)}`;
    expect(previewRef({ dir: `/tmp/${staged}` })).not.toBe(previewRef({ dir: `/tmp/${staged}x` }));
  });

  it("resolves two spellings of one directory to one pointer", () => {
    expect(previewRef({ dir: "/tmp/a/" })).toBe(previewRef({ dir: "/tmp/a" }));
  });

  it("refuses to invent a pointer from nothing", () => {
    expect(() => previewRef({ dir: "" })).toThrow(/needs a directory/);
  });
});

describe("previewLabelProblem", () => {
  it("passes for the longest run id GitHub mints", () => {
    const slug = projectSlug({ runId: "31599563227" });
    expect(previewLabelProblem(slug, previewRef({ dir: `/tmp/node-e2e-${"a".repeat(64)}` }))).toEqual([]);
  });

  it("passes for the longest slug the budget allows", () => {
    const slug = projectSlug({ runId: "9".repeat(200) });
    expect(slug.length).toBe(MAX_SLUG_LEN);
    expect(previewLabelProblem(slug, previewRef({ dir: `/tmp/${"z".repeat(64)}` }))).toEqual([]);
  });

  it("names every app whose hostname would be over the DNS cap", () => {
    const problems = previewLabelProblem("s".repeat(60), "pointer");
    expect(problems).toHaveLength(SMOKE_APPS.length);
    for (const app of SMOKE_APPS) {
      expect(problems.some((problem) => problem.includes(`app ${app.name}`))).toBe(true);
    }
  });

  it("keeps every app's hostname inside one DNS label", () => {
    for (const app of SMOKE_APPS) {
      expect(previewLabel(projectSlug({ runId: "9".repeat(200) }), "z".repeat(19), app.name).length)
        .toBeLessThanOrEqual(PREVIEW_LABEL_MAX_LEN);
    }
  });
});

describe("renderOcelConfig", () => {
  it("declares the AWS provider, the cloudflare edge and every smoke app with its framework", () => {
    const config = renderOcelConfig({ slug: "e2en-7" });
    expect(config).toContain(`slug: "e2en-7"`);
    expect(config).toContain("awsProvider()");
    expect(config).toContain(`import { cfEdge } from "ocel/edge";`);
    expect(config).toContain("edge: cfEdge()");
    for (const app of SMOKE_APPS) {
      expect(config).toContain(`name: "${app.name}"`);
      expect(config).toContain(`framework: "${app.framework}"`);
      expect(config).toContain(`path: "${app.path}"`);
    }
  });
});

describe("smoke app", () => {
  it("serves the marker the assertion looks for", () => {
    for (const app of SMOKE_APPS) {
      const source = readFileSync(join(here, "smoke-app", app.path, "src", "server.ts"), "utf8");
      expect(source).toContain(`"${serveMarker(app.framework)}"`);
    }
  });

  it("declares the dependency the framework is detected by", () => {
    for (const app of SMOKE_APPS) {
      const pkg = JSON.parse(readFileSync(join(here, "smoke-app", app.path, "package.json"), "utf8"));
      expect(pkg.dependencies?.[app.framework]).toBeTruthy();
      expect(pkg.type).toBe("module");
    }
  });

  it("covers both node frameworks the deploy path had no coverage for", () => {
    expect(SMOKE_APPS.map((app) => app.framework).sort()).toEqual(["express", "hono"]);
  });
});

describe("isFunctionURLHost", () => {
  it("recognises a Lambda Function URL, whatever the region", () => {
    expect(isFunctionURLHost("abc123.lambda-url.us-east-1.on.aws")).toBe(true);
    expect(isFunctionURLHost("ABC123.LAMBDA-URL.EU-WEST-2.ON.AWS")).toBe(true);
  });

  it("does not mistake an edge hostname for one", () => {
    expect(isFunctionURLHost("e2en-7--ptr-abcdef12--exp.ocel.site")).toBe(false);
    expect(isFunctionURLHost("lambda-url.on.aws.example.com")).toBe(false);
  });
});

describe("resolveAppURLs", () => {
  const slug = "e2en-7";
  const pointer = "node-e2e-a-abcdef12";

  it("attributes each app's edge URL by its own preview hostname, not by position", () => {
    const urls = [...SMOKE_APPS].reverse().map((app) => edgeURL(slug, pointer, app.name));
    const { resolved, unattributed } = resolveAppURLs({ appUrls: urls }, { slug, pointer });
    expect(unattributed).toEqual([]);
    expect(resolved.map((entry) => [entry.app, entry.kind])).toEqual(
      SMOKE_APPS.map((app) => [app.name, "edge"]),
    );
    for (const entry of resolved) {
      expect(entry.url).toBe(edgeURL(slug, pointer, entry.app));
    }
  });

  it("leaves an app that got no worker with no URL, and strands the Function URL the deploy fell back to", () => {
    const urls = [
      edgeURL(slug, pointer, "exp"),
      "https://qz7.lambda-url.us-east-1.on.aws/",
    ];
    const { resolved, unattributed } = resolveAppURLs({ appUrls: urls }, { slug, pointer });
    expect(resolved.find((entry) => entry.app === "hono")).toMatchObject({ url: null, kind: "missing" });
    expect(unattributed).toEqual(["https://qz7.lambda-url.us-east-1.on.aws/"]);
  });

  it("survives a deploy result with no appUrls at all", () => {
    const { resolved, unattributed } = resolveAppURLs({}, { slug, pointer });
    expect(unattributed).toEqual([]);
    expect(resolved.every((entry) => entry.kind === "missing")).toBe(true);
  });
});

describe("edgeVerdict", () => {
  const headers = new Headers({ "cf-ray": "8f0c-LHR" });

  it("accepts an edge hostname that answered with a cf-ray", () => {
    expect(edgeVerdict({ url: "https://e2en-7--p--exp.ocel.site/", status: 200, headers }).kind).toBe("edge");
  });

  it("rejects a Function URL, naming why it could never serve a public request", () => {
    const verdict = edgeVerdict({ url: "https://qz7.lambda-url.us-east-1.on.aws/", status: 200, headers });
    expect(verdict.kind).toBe("function-url");
    expect(verdict.detail).toContain("AuthorizationType: IAM");
  });

  it("rejects a 200 that Cloudflare never touched", () => {
    expect(edgeVerdict({ url: "https://x.ocel.site/", status: 200, headers: {} }).kind).toBe("not-cloudflare");
  });

  it("reads plain header objects case-insensitively", () => {
    expect(edgeVerdict({ url: "https://x.ocel.site/", status: 200, headers: { "CF-Ray": "8f0c" } }).kind)
      .toBe("edge");
  });

  it("rejects something that is not a URL", () => {
    expect(edgeVerdict({ url: "not a url", status: 200, headers }).kind).toBe("no-url");
  });
});

describe("echoMismatches", () => {
  const sent = { framework: "express", ...echoRequest("abc") };

  const intact = {
    framework: sent.framework,
    method: "POST",
    path: sent.path,
    query: sent.query,
    probeHeader: sent.header.value,
    body: sent.body,
  };

  it("passes when the whole request survived the worker", () => {
    expect(echoMismatches(sent, intact)).toEqual([]);
  });

  it("catches a dropped query parameter", () => {
    const problems = echoMismatches(sent, { ...intact, query: { ...intact.query, b: undefined } });
    expect(problems).toHaveLength(1);
    expect(problems[0]).toContain("query b");
  });

  it("catches a dropped header", () => {
    expect(echoMismatches(sent, { ...intact, probeHeader: null })[0]).toContain("x-ocel-probe");
  });

  it("catches a truncated body", () => {
    expect(echoMismatches(sent, { ...intact, body: { stamp: "abc" } })[0]).toContain("body");
  });

  it("catches a method the worker rewrote", () => {
    expect(echoMismatches(sent, { ...intact, method: "GET" })[0]).toContain("method");
  });

  it("catches a path the worker flattened", () => {
    expect(echoMismatches(sent, { ...intact, path: "/echo" })[0]).toContain("path");
  });

  it("catches an answer from the wrong app", () => {
    expect(echoMismatches(sent, { ...intact, framework: "hono" })[0]).toContain("framework");
  });

  it("says everything that is wrong, not just the first thing", () => {
    expect(echoMismatches(sent, {})).toHaveLength(
      3 + Object.keys(sent.query).length + 2,
    );
  });
});

describe("strandedProjectSlugs", () => {
  it("claims this suite's projects and leaves the next suite's alone", () => {
    const names = [
      `${PREVIEW_ROOT_STACK_PARAM_PREFIX}${SLUG_PREFIX}1`,
      `${PREVIEW_ROOT_STACK_PARAM_PREFIX}${SLUG_PREFIX}2`,
      `${PREVIEW_ROOT_STACK_PARAM_PREFIX}${NEXT_SLUG_PREFIX}3`,
      `${PREVIEW_ROOT_STACK_PARAM_PREFIX}someones-real-project`,
      "/ocel/something-else/x",
    ];
    expect(strandedProjectSlugs(names, `${SLUG_PREFIX}2`)).toEqual([`${SLUG_PREFIX}1`]);
  });

  it("survives an empty list", () => {
    expect(strandedProjectSlugs(undefined, "x")).toEqual([]);
  });
});

describe("envSegment", () => {
  it("names the preview environment the lambda is tagged with", () => {
    expect(envSegment({ class: "preview", identity: "ptr" })).toBe("preview-ptr");
    expect(envSegment({ class: "production" })).toBe("prod");
    expect(envSegment(undefined)).toBe("prod");
  });
});

describe("lambdaFunctionNames", () => {
  it("reads function names out of a tagging API response", () => {
    expect(
      lambdaFunctionNames({
        ResourceTagMappingList: [
          { ResourceARN: "arn:aws:lambda:us-east-1:1:function:ocel-e2en-7-exp" },
          { ResourceARN: "arn:aws:s3:::bucket" },
        ],
      }),
    ).toEqual(["ocel-e2en-7-exp"]);
  });
});

describe("toolchainArtifacts", () => {
  it("names the node half, the CLI and both provider binaries, each with the command that builds it", () => {
    const artifacts = toolchainArtifacts("/repo");
    expect(artifacts).toHaveLength(4);
    for (const artifact of artifacts) {
      expect(artifact.path.startsWith("/repo/")).toBe(true);
      expect(artifact.how).toBeTruthy();
    }
    expect(artifacts.some((a) => a.path.includes("packages/ocel/dist"))).toBe(true);
    expect(artifacts.some((a) => a.path.includes("/deploy"))).toBe(true);
    expect(artifacts.some((a) => a.path.includes("/runtime"))).toBe(true);
  });
});

describe("urlHost", () => {
  it("lowercases the host and drops everything else", () => {
    expect(urlHost("https://Host.Example/path?q=1")).toBe("host.example");
  });

  it("is empty for something that is not a URL", () => {
    expect(urlHost("nope")).toBe("");
  });
});

describe("bytecodeSettings", () => {
  it("reads the coordinate the deploy put on the function", () => {
    expect(
      bytecodeSettings({
        OCEL_BYTECODE_BUCKET: "assets-xyz",
        OCEL_BYTECODE_PREFIX: "preview-ptr/e2en-7/exp/rdeadbeef/bytecode/",
      }),
    ).toMatchObject({ kind: "present", bucket: "assets-xyz", prefix: "preview-ptr/e2en-7/exp/rdeadbeef/bytecode" });
  });

  it("calls a function carrying neither var absent — the shape a deploy with the flag off leaves", () => {
    const settings = bytecodeSettings({ OCEL_ISR_BUCKET: "assets-xyz" });
    expect(settings.kind).toBe("absent");
    expect(settings.missing).toEqual(["OCEL_BYTECODE_BUCKET", "OCEL_BYTECODE_PREFIX"]);
  });

  it("names the half that is missing, since half a coordinate resolves to no cache at all", () => {
    expect(bytecodeSettings({ OCEL_BYTECODE_PREFIX: "p/q/app/r1/bytecode" })).toMatchObject({
      kind: "partial",
      missing: ["OCEL_BYTECODE_BUCKET"],
    });
  });
});

describe("bytecodePrefixProblem", () => {
  const environment = { class: "preview", identity: "ptr" };

  it("passes the app's own bytecode coordinate", () => {
    expect(
      bytecodePrefixProblem({
        prefix: "preview-ptr/e2en-7/exp/rdeadbeef/bytecode",
        environment,
        slug: "e2en-7",
        app: "exp",
      }),
    ).toBeNull();
  });

  it("catches a prefix belonging to another app", () => {
    expect(
      bytecodePrefixProblem({
        prefix: "preview-ptr/e2en-7/hono/rdeadbeef/bytecode",
        environment,
        slug: "e2en-7",
        app: "exp",
      }),
    ).toContain("does not name this app");
  });

  it("catches the ISR prefix, which is what bytecode used to borrow", () => {
    expect(
      bytecodePrefixProblem({
        prefix: "preview-ptr/e2en-7/exp/rdeadbeef/isr",
        environment,
        slug: "e2en-7",
        app: "exp",
      }),
    ).toContain("ends in isr");
  });
});

describe("bytecodeArchiveName", () => {
  it("reads the node version and architecture out of a cache key", () => {
    expect(bytecodeArchiveName("preview-ptr/e2en-7/exp/r1/bytecode/fn/node24.3.1-arm64.tar.gz")).toMatchObject({
      nodeVersion: "24.3.1",
      arch: "arm64",
    });
  });

  it("is null for anything that is not a cache archive", () => {
    expect(bytecodeArchiveName("preview-ptr/e2en-7/exp/r1/bytecode/")).toBeNull();
    expect(bytecodeArchiveName("some/other/object.zip")).toBeNull();
  });
});

describe("bytecodeRehydrateOutcome", () => {
  const key = "preview-ptr/e2en-7/exp/r1/bytecode/fn/node24.3.1-arm64.tar.gz";

  it("reads the hit the bootstrap logs on a cold start", () => {
    expect(
      bytecodeRehydrateOutcome(`ocel: rehydrated compile cache from ${key}: 63963136 bytes in 412ms`, key),
    ).toMatchObject({ kind: "hit" });
  });

  it("tells a miss, a disabled cache and an embedded load apart", () => {
    expect(bytecodeRehydrateOutcome(`ocel: no compile cache at ${key} yet; nothing to rehydrate`, key).kind).toBe(
      "miss",
    );
    expect(bytecodeRehydrateOutcome("ocel: could not read node's version, compile cache disabled: x", key).kind).toBe(
      "disabled",
    );
    expect(
      bytecodeRehydrateOutcome("ocel: loaded embedded compile cache from .ocel/bytecode/node24.3.1-arm64.tar: 1 bytes in 2ms", key)
        .kind,
    ).toBe("embedded");
  });

  it("keeps a compile-cache line naming some other key, so the summary cannot claim silence", () => {
    expect(bytecodeRehydrateOutcome("ocel: no compile cache at other/key yet; nothing to rehydrate", key).kind).toBe(
      "other-key",
    );
  });

  it("ignores lines that are not about the compile cache", () => {
    expect(bytecodeRehydrateOutcome("START RequestId: abc", key)).toBeNull();
  });
});

describe("summarizeOutcomes", () => {
  it("counts by kind", () => {
    expect(summarizeOutcomes([{ kind: "miss" }, { kind: "miss" }, { kind: "disabled" }])).toBe("2 miss, 1 disabled");
  });

  it("says so when nothing at all was seen", () => {
    expect(summarizeOutcomes([])).toContain("no compile-cache lines");
  });
});

describe("tail", () => {
  it("keeps the last lines", () => {
    expect(tail("a\nb\nc", 2)).toBe("b\nc");
    expect(tail("a", 5)).toBe("a");
  });
});
