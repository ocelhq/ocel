import { afterEach, describe, expect, it, vi } from "vitest";
import { type Adapters, type AppBuild, buildApps } from "./apps.js";
import { PROTOCOL_PREFIX } from "./protocol.js";

function records(): { type: string; app?: string; ok?: boolean }[] {
  const out: { type: string; app?: string; ok?: boolean }[] = [];
  vi.spyOn(process.stdout, "write").mockImplementation((chunk: unknown) => {
    const line = String(chunk);
    if (line.startsWith(`\n${PROTOCOL_PREFIX}`)) {
      out.push(JSON.parse(line.slice(1 + PROTOCOL_PREFIX.length)));
    }
    return true;
  });
  return out;
}

afterEach(() => vi.restoreAllMocks());

const next: AppBuild = {
  framework: "next",
  name: "web",
  cwd: "/p/web",
  outputDir: "/p/.ocel/output/apps/web",
  buildId: "0123456789abcdef0123456789abcdef",
};

const node: AppBuild = {
  framework: "node",
  name: "api",
  cwd: "/p/api",
  entrypoint: "/p/api/src/server.ts",
  functionDir: "/p/.ocel/output/apps/api/functions/index.func",
};

function recording(): Adapters & { calls: string[] } {
  const calls: string[] = [];
  return {
    calls,
    next: async (app) => void calls.push(`next:${app.name}`),
    node: async (trace) => void calls.push(`node:${trace.functionDir}`),
  };
}

describe("buildApps", () => {
  it("hands each app to its framework's adapter, in order", async () => {
    records();
    const adapters = recording();

    await buildApps([next, node], adapters);

    expect(adapters.calls).toEqual(["next:web", `node:${node.functionDir}`]);
  });

  it("opens and ends a build span named for each app", async () => {
    const seen = records();

    await buildApps([next, node], recording());

    expect(seen.map((r) => [r.type, r.app ?? "", r.ok ?? ""])).toEqual([
      ["span_start", "web", ""],
      ["span_end", "", true],
      ["span_start", "api", ""],
      ["span_end", "", true],
    ]);
  });

  it("stops at the first app that fails, naming it", async () => {
    const seen = records();
    const adapters = recording();
    adapters.next = async () => {
      throw new Error("next build exited with code 1");
    };

    await expect(buildApps([next, node], adapters)).rejects.toThrow("exited with code 1");
    expect(adapters.calls).toEqual([]);
    expect(seen.find((r) => r.type === "error")?.app).toBe("web");
  });

  it("refuses a framework it has no adapter for", async () => {
    records();
    const unknown = { ...node, framework: "remix" } as unknown as AppBuild;

    await expect(buildApps([unknown], recording())).rejects.toThrow(/"remix"/);
  });
});
