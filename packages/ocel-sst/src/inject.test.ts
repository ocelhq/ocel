import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";

interface Built {
  name: string;
  props: Record<string, unknown>;
}

const built: Built[] = [];

const util = {
  getStack: () => "production",
  getProject: () => "shop",
  dynamic: {
    Resource: class {
      constructor(_provider: unknown, name: string, props: Record<string, unknown>) {
        built.push({ name, props });
      }
    },
  },
};

const root = "/repo/app";

let outDir: string;
let link: typeof import("./index.js").link;

beforeAll(async () => {
  const { build } = await import("vite");
  outDir = await mkdtemp(join(tmpdir(), "ocel-sst-inject-"));
  await build({
    logLevel: "silent",
    define: {
      $util: "globalThis.__sstInjectedUtil",
      $cli: JSON.stringify({ paths: { root } }),
    },
    build: {
      ssr: true,
      outDir,
      emptyOutDir: true,
      lib: { entry: join(import.meta.dirname, "index.ts"), formats: ["es"], fileName: "index" },
    },
  });
  (globalThis as { __sstInjectedUtil?: unknown }).__sstInjectedUtil = util;
  ({ link } = await import(pathToFileURL(join(outDir, "index.js")).href));
}, 60_000);

afterAll(async () => {
  await rm(outDir, { recursive: true, force: true });
});

beforeEach(() => {
  built.length = 0;
  expect((globalThis as { $util?: unknown }).$util).toBeUndefined();
  expect((globalThis as { $cli?: unknown }).$cli).toBeUndefined();
});

describe("reaching what SST injects into the config bundle", () => {
  it("declares a postgres link from the injected util", () => {
    link.postgres("orders", {
      host: "orders.internal",
      port: 5432,
      database: "orders",
      username: "operator",
      password: "hunter2",
    });

    expect(built).toHaveLength(1);
    expect(built[0]?.name).toBe("ocel-link-orders");
    expect(built[0]?.props).toMatchObject({ project: root, class: "production" });
  });

  it("declares a custom link from the injected util", () => {
    link.custom("network", { properties: { subnetIds: ["subnet-1"] } });

    expect(built).toHaveLength(1);
    expect(built[0]?.props).toMatchObject({ project: root, name: "network" });
  });
});
