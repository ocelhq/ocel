import { describe, expect, it } from "vitest";
import { hop, type Spawn } from "./hop.js";
import type { PublishRequest } from "./hop.js";

const request: PublishRequest = {
  project: "acme",
  publisher: "sst:OcelLinks",
  class: "production",
  records: [
    { name: "orders", type: "sst:aws.Postgres", properties: { host: "h" } },
  ],
};

function spawning(result: {
  status: number | null;
  stdout?: string;
  stderr?: string;
}): { spawn: Spawn; seen: { command?: string; args?: string[]; input?: string } } {
  const seen: { command?: string; args?: string[]; input?: string } = {};
  const spawn: Spawn = (command, args, input) => {
    seen.command = command;
    seen.args = args;
    seen.input = input;
    return {
      status: result.status,
      stdout: result.stdout ?? "",
      stderr: result.stderr ?? "",
    };
  };
  return { spawn, seen };
}

describe("hop", () => {
  it("hands the request to the publisher on stdin and reads its answer", () => {
    const { spawn, seen } = spawning({
      status: 0,
      stdout: '{"published":["orders"],"pruned":0,"table":"ocel-vars"}\n',
    });

    const response = hop("publish-links", request, {
      spawn,
      resolve: () => "/bin/deploy",
    });

    expect(seen.command).toBe("/bin/deploy");
    expect(seen.args).toEqual(["publish-links"]);
    expect(JSON.parse(seen.input!)).toEqual(request);
    expect(response.published).toEqual(["orders"]);
  });

  it("carries no ocel token, only the project it addresses", () => {
    const { spawn, seen } = spawning({ status: 0, stdout: "{}" });

    hop("publish-links", request, { spawn, resolve: () => "/bin/deploy" });

    expect(JSON.parse(seen.input!)).not.toHaveProperty("token");
    expect(JSON.parse(seen.input!).project).toBe("acme");
  });

  it("surfaces what the publisher refused, not an exit code", () => {
    const { spawn } = spawning({
      status: 1,
      stderr:
        "ocel aws provider: this AWS account holds no ocel production substrate, so the links this stack publishes have nowhere to land\n",
    });

    expect(() =>
      hop("publish-links", request, { spawn, resolve: () => "/bin/deploy" }),
    ).toThrow(/no ocel production substrate/);
  });

  it("names the missing publisher rather than a module resolution failure", () => {
    const { spawn } = spawning({ status: 0, stdout: "{}" });

    expect(() =>
      hop("publish-links", request, {
        spawn,
        resolve: () => {
          throw new Error("Cannot find module");
        },
      }),
    ).toThrow(/@ocel\/sst/);
  });

  it("refuses an answer it cannot read", () => {
    const { spawn } = spawning({ status: 0, stdout: "not json" });

    expect(() =>
      hop("publish-links", request, { spawn, resolve: () => "/bin/deploy" }),
    ).toThrow(/read/i);
  });
});
