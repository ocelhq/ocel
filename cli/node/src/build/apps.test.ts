import { afterEach, describe, expect, it, vi } from "vitest";
import { type Adapters, type AppBuild, buildApps } from "./apps.js";
import { PROTOCOL_PREFIX } from "./protocol.js";

interface SeenRecord {
  type: string;
  app?: string;
  ok?: boolean;
  message?: string;
}

function records(): SeenRecord[] {
  const out: SeenRecord[] = [];
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

const sveltekit: AppBuild = {
  framework: "sveltekit",
  name: "shop",
  cwd: "/p/shop",
  outputDir: "/p/.ocel/output/apps/shop",
  buildId: "fedcba9876543210fedcba9876543210",
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
    sveltekit: async (app) => void calls.push(`sveltekit:${app.name}`),
    node: async (trace) => void calls.push(`node:${trace.functionDir}`),
  };
}

function gate(): { opened: Promise<void>; open: () => void } {
  let open = () => {};
  const opened = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { opened, open };
}

describe("buildApps", () => {
  it("hands each app to its framework's adapter, in order", async () => {
    records();
    const adapters = recording();

    await buildApps([next, sveltekit, node], adapters, 1);

    expect(adapters.calls).toEqual(["next:web", "sveltekit:shop", `node:${node.functionDir}`]);
  });

  it("opens and ends a build span named for each app", async () => {
    const seen = records();

    await buildApps([next, node], recording(), 1);

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

    await expect(buildApps([next, node], adapters, 1)).rejects.toThrow("exited with code 1");
    expect(adapters.calls).toEqual([]);
    expect(seen.find((r) => r.type === "error")?.app).toBe("web");
  });

  it("refuses a framework it has no adapter for", async () => {
    records();
    const unknown = { ...node, framework: "remix" } as unknown as AppBuild;

    await expect(buildApps([unknown], recording(), 1)).rejects.toThrow(/"remix"/);
  });

  it("names the app on every line its build prints, while both apps build at once", async () => {
    const seen = records();
    const bothStarted = gate();
    let started = 0;
    const adapters = recording();
    adapters.next = async (app, log) => {
      started++;
      if (started === 2) bothStarted.open();
      await bothStarted.opened;
      log.print(`compiling ${app.name}`);
    };
    adapters.sveltekit = async (app, log) => {
      started++;
      if (started === 2) bothStarted.open();
      await bothStarted.opened;
      log.print(`bundling ${app.name}`);
    };

    await buildApps([next, sveltekit], adapters, 2);

    const output = seen.filter((r) => r.type === "output");
    expect(output).toEqual(
      expect.arrayContaining([
        { type: "output", app: "web", message: "compiling web" },
        { type: "output", app: "shop", message: "bundling shop" },
      ]),
    );
    expect(output).toHaveLength(2);
  });

  it("builds apps that share a directory one after another, since their builds write into it", async () => {
    records();
    const sameDir: AppBuild = { ...next, name: "web-eu", outputDir: "/p/.ocel/output/apps/web-eu" };
    let building = 0;
    let most = 0;
    const adapters = recording();
    adapters.next = async () => {
      building++;
      most = Math.max(most, building);
      await new Promise((resolve) => setTimeout(resolve, 10));
      building--;
    };

    await buildApps([next, sameDir], adapters, 2);

    expect(most).toBe(1);
  });

  it("starts no app after one fails, and lets the builds already running end before naming the failure", async () => {
    const seen = records();
    const shopStarted = gate();
    const webFailed = gate();
    const adapters = recording();
    adapters.next = async () => {
      await shopStarted.opened;
      webFailed.open();
      throw new Error("next build exited with code 1");
    };
    adapters.sveltekit = async (app) => {
      shopStarted.open();
      await webFailed.opened;
      adapters.calls.push(`sveltekit:${app.name}`);
    };

    await expect(buildApps([next, sveltekit, node], adapters, 2)).rejects.toThrow(
      "exited with code 1",
    );

    expect(adapters.calls).toEqual(["sveltekit:shop"]);
    expect(seen.filter((r) => r.type === "span_end").map((r) => r.ok)).toEqual([false, true]);
  });
});
