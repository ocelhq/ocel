import { describe, expect, it } from "vitest";

import { PINNED } from "../../matrix.config.mjs";
import {
  BASIC_EXECUTION_POLICY_ARN,
  BUNDLE_FILE,
  ESBUILD_VERSION,
  LAMBDA_HANDLER,
  LAMBDA_NAME_MAX,
  cellToken,
  logGroupName,
  pinProblems,
  rawFunctionName,
  rawRoleName,
  reclaimMessage,
  urlProblems,
} from "./lib.mjs";

const LIVE = { Runtime: "nodejs24.x", MemorySize: 1024, Architectures: ["x86_64"] };

describe("cellToken", () => {
  it("separates two workdirs with no run id", () => {
    expect(cellToken({ workdir: "/s/express" })).not.toBe(cellToken({ workdir: "/s/hono" }));
  });

  it("ignores a trailing slash", () => {
    expect(cellToken({ workdir: "/s/express/" })).toBe(cellToken({ workdir: "/s/express" }));
  });

  it("separates two runs staged at the same path", () => {
    expect(cellToken({ workdir: "/s/express", runId: "111" })).not.toBe(
      cellToken({ workdir: "/s/express", runId: "222" }),
    );
  });

  it("is stable, so teardown can re-derive it", () => {
    expect(cellToken({ workdir: "/s/express", runId: "abc" })).toBe(
      cellToken({ workdir: "/s/express", runId: "abc" }),
    );
  });
});

describe("rawFunctionName", () => {
  it("names every matrix app distinctly and legally", () => {
    const names = ["express", "hono", "fastify"].map((app) =>
      rawFunctionName({ app, workdir: `/staged/${app}`, runId: "17123456789" }),
    );
    expect(new Set(names).size).toBe(3);
    for (const name of names) {
      expect(name.length).toBeLessThanOrEqual(LAMBDA_NAME_MAX);
      expect(name).toMatch(/^[a-zA-Z0-9-_]+$/);
    }
  });

  it("derives the role and log group from the function name", () => {
    const name = rawFunctionName({ app: "hono", workdir: "/staged/hono" });
    expect(rawRoleName(name)).toBe(`${name}-role`);
    expect(logGroupName(name)).toBe(`/aws/lambda/${name}`);
  });
});

describe("pinProblems", () => {
  it("passes the pinned shape", () => {
    expect(pinProblems(LIVE, PINNED)).toEqual([]);
  });

  it("catches a graviton arm function", () => {
    expect(pinProblems({ ...LIVE, Architectures: ["arm64"] }, PINNED)).toEqual([
      `architecture is "arm64", not the pinned "x86_64"`,
    ]);
  });

  it("catches an unpinned memory default", () => {
    expect(pinProblems({ ...LIVE, MemorySize: 1769 }, PINNED)).toEqual([
      `memory is 1769 MB, not the pinned 1024 MB`,
    ]);
  });

  it("catches a stale runtime", () => {
    expect(pinProblems({ ...LIVE, Runtime: "nodejs22.x" }, PINNED)).toEqual([
      `runtime is "nodejs22.x", not the pinned "nodejs24.x"`,
    ]);
  });

  it("reports every drift at once", () => {
    expect(pinProblems({ Runtime: "nodejs20.x", MemorySize: 128, Architectures: ["arm64"] }, PINNED)).toHaveLength(3);
  });
});

describe("urlProblems", () => {
  it("passes a public function URL", () => {
    expect(urlProblems({ AuthType: "NONE", FunctionUrl: "https://x.lambda-url.us-east-1.on.aws/" })).toEqual([]);
  });

  it("catches an IAM-signed URL", () => {
    expect(urlProblems({ AuthType: "AWS_IAM", FunctionUrl: "https://x.lambda-url.us-east-1.on.aws/" })).toEqual([
      `the function URL is AuthType "AWS_IAM", not NONE, so it will 403`,
    ]);
  });
});

describe("reclaimMessage", () => {
  it("names the function, the reason and the exact reclaim commands", () => {
    const message = reclaimMessage({
      functionName: "bench-raw-hono-abc",
      roleName: "bench-raw-hono-abc-role",
      roleCreated: true,
      region: "us-east-1",
      problems: ["could not delete the function bench-raw-hono-abc: throttled"],
    });
    expect(message).toContain("TEARDOWN FAILED");
    expect(message).toContain("still live and still billable");
    expect(message).toContain("aws lambda delete-function --region us-east-1 --function-name bench-raw-hono-abc");
    expect(message).toContain("aws logs delete-log-group --region us-east-1 --log-group-name /aws/lambda/bench-raw-hono-abc");
    expect(message).toContain(`aws iam detach-role-policy --role-name bench-raw-hono-abc-role --policy-arn ${BASIC_EXECUTION_POLICY_ARN}`);
    expect(message).toContain("aws iam delete-role --role-name bench-raw-hono-abc-role");
  });

  it("does not offer to delete a role it never created", () => {
    const message = reclaimMessage({
      functionName: "f",
      roleName: "r",
      roleCreated: false,
      region: "us-east-1",
      problems: ["nope"],
    });
    expect(message).not.toContain("delete-role");
  });
});

describe("bundle coordinates", () => {
  it("points Lambda at the handler export the apps publish", () => {
    expect(LAMBDA_HANDLER).toBe("index.handler");
  });

  it("names the zip entry the handler string resolves to", () => {
    expect(LAMBDA_HANDLER.split(".")[0]).toBe(BUNDLE_FILE.replace(/\.mjs$/, ""));
  });

  it("pins the bundler so buildMs is reproducible run to run", () => {
    expect(ESBUILD_VERSION).toMatch(/^\d+\.\d+\.\d+$/);
  });
});
