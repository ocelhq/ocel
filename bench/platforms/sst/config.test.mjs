import { describe, expect, it } from "vitest";

import { APPS, PINNED } from "../../matrix.config.mjs";
import {
  BOOTSTRAP_PARAMETER,
  COMPONENT,
  HANDLER,
  TIMEOUT_SECONDS,
  outputsProblems,
  passphraseParameter,
  pinProblems,
  reclaimMessage,
  renderSstConfig,
  sstAppName,
  sstStage,
} from "./config.mjs";

const RENDERED = renderSstConfig({ appName: "bench-express", region: "eu-west-1", pinned: PINNED });

describe("renderSstConfig", () => {
  it("pins the runtime, memory and architecture the matrix froze", () => {
    expect(RENDERED).toContain(`runtime: "nodejs24.x"`);
    expect(RENDERED).toContain(`memory: "1024 MB"`);
    expect(RENDERED).toContain(`architecture: "x86_64"`);
  });

  it("does not leave any of the three to an SST default", () => {
    for (const key of ["runtime", "memory", "architecture"]) {
      expect(RENDERED).toContain(`${key}: `);
    }
  });

  it("asks for a public function URL, not an api gateway", () => {
    expect(RENDERED).toContain(`url: { authorization: "none" }`);
    expect(RENDERED).not.toContain("ApiGatewayV2");
    expect(RENDERED).not.toContain("Router");
  });

  it("points at the lambda entrypoint the apps publish", () => {
    expect(HANDLER).toBe("src/handler.handler");
    expect(RENDERED).toContain(`handler: "src/handler.handler"`);
  });

  it("keeps state in aws and takes everything back on remove", () => {
    expect(RENDERED).toContain(`home: "aws"`);
    expect(RENDERED).toContain(`removal: "remove"`);
  });

  it("pins the provider to the benchmark region", () => {
    expect(RENDERED).toContain(`providers: { aws: { region: "eu-west-1" } }`);
  });

  it("returns the deployed function name as an output, not just the url", () => {
    expect(RENDERED).toContain(`return { functionName: fn.name, url: fn.url };`);
    expect(RENDERED).toContain(`new sst.aws.Function(${JSON.stringify(COMPONENT)}`);
  });

  it("gives the function the same timeout the raw baseline gets", () => {
    expect(RENDERED).toContain(`timeout: "${TIMEOUT_SECONDS} seconds"`);
  });
});

describe("stage and app naming", () => {
  it("gives every matrix app its own sst app so the three cells never collide", () => {
    const names = APPS.map((app) => sstAppName(app.name));
    expect(new Set(names).size).toBe(APPS.length);
    for (const name of names) expect(name).toMatch(/^[a-z0-9-]+$/);
  });

  it("gives two workdirs two stages even with no run id", () => {
    expect(sstStage({ workdir: "/s/express" })).not.toBe(sstStage({ workdir: "/s/hono" }));
  });

  it("gives two runs two stages at the same workdir", () => {
    expect(sstStage({ workdir: "/s/express", runId: "111" })).not.toBe(
      sstStage({ workdir: "/s/express", runId: "222" }),
    );
  });

  it("is stable so teardown can re-derive the stage", () => {
    expect(sstStage({ workdir: "/s/express", runId: "abc" })).toBe(sstStage({ workdir: "/s/express", runId: "abc" }));
  });

  it("starts with a letter and stays a legal stage name", () => {
    const stage = sstStage({ workdir: "/s/express", runId: "17123456789" });
    expect(stage).toMatch(/^[a-z][a-z0-9-]*$/);
    expect(stage.length).toBeLessThanOrEqual(20);
  });

  it("scopes the passphrase parameter to the app and stage", () => {
    expect(passphraseParameter("bench-hono", "sabc")).toBe("/sst/passphrase/bench-hono/sabc");
  });
});

describe("outputsProblems", () => {
  it("accepts what run() returns", () => {
    expect(
      outputsProblems({ functionName: "bench-express-s1-BenchFunction-x", url: "https://x.lambda-url.on.aws/" }),
    ).toEqual([]);
  });

  it("refuses outputs with only a url", () => {
    const problems = outputsProblems({ url: "https://x.lambda-url.on.aws/" });
    expect(problems).toHaveLength(1);
    expect(problems[0]).toContain("no cold start can be forced");
  });

  it("refuses a missing outputs file", () => {
    expect(outputsProblems(null)).toHaveLength(1);
  });
});

describe("pinProblems", () => {
  it("passes the pinned shape", () => {
    expect(pinProblems({ Runtime: "nodejs24.x", MemorySize: 1024, Architectures: ["x86_64"] }, PINNED)).toEqual([]);
  });

  it("catches an arm function at a different memory", () => {
    expect(
      pinProblems({ Runtime: "nodejs24.x", MemorySize: 1769, Architectures: ["arm64"] }, PINNED),
    ).toHaveLength(2);
  });
});

describe("reclaimMessage", () => {
  it("names the stage, the reclaim command and the bootstrap that outlives it", () => {
    const message = reclaimMessage({
      appName: "bench-hono",
      stage: "sabc",
      region: "us-east-1",
      workdir: "/staged/hono",
      problems: ["could not remove stage sabc: exited with 1"],
    });
    expect(message).toContain("TEARDOWN FAILED");
    expect(message).toContain("still live and still billable");
    expect(message).toContain("remove --stage sabc");
    expect(message).toContain("/sst/passphrase/bench-hono/sabc");
    expect(message).toContain(BOOTSTRAP_PARAMETER);
    expect(message).toContain("removeBootstrap()");
  });
});
