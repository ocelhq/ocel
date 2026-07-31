import { readFile } from "node:fs/promises";
import { dirname, join, resolve as resolvePath } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";
import { z } from "zod";

const {
  defineEnv,
  EnvDefinitionError,
  EnvEdgeError,
  EnvScopeError,
  EnvValueError,
} = await import("./edge.js");

const here = dirname(fileURLToPath(import.meta.url));

const ENTRY_GLOBAL = "__OCEL_EDGE_ENTRY";

afterEach(() => {
  delete (globalThis as Record<string, unknown>)[ENTRY_GLOBAL];
});

describe("declaring on the edge", () => {
  // A definitions module is normally shared by node and edge entries, so the
  // edge build must be able to evaluate one. Only the read is refused.
  it("is harmless: declaring never throws", () => {
    expect(() =>
      defineEnv({ EDGE_HARMLESS: { class: "plain", schema: z.string() } }),
    ).not.toThrow();
  });

  it("still refuses a definition the node build would refuse", () => {
    expect(() =>
      defineEnv({ "not a key": { class: "plain" } } as never),
    ).toThrow(EnvDefinitionError);
    expect(() =>
      defineEnv({ OCEL_TAKEN: { class: "plain" } }),
    ).toThrow(EnvDefinitionError);
  });

  // Symbols are how the runtime inspects an object; they are never a variable,
  // and answering them is what keeps the object loggable — same rule the node
  // build follows.
  it("answers symbol reads with undefined rather than throwing", () => {
    const env = defineEnv({ EDGE_SYMBOL: { class: "plain" } }) as Record<
      symbol,
      unknown
    >;
    expect(env[Symbol.toStringTag]).toBeUndefined();
    expect(env[Symbol.for("nodejs.util.inspect.custom")]).toBeUndefined();
  });
});

describe("reading on the edge", () => {
  it("throws naming the key, the tier and the one real remedy", () => {
    const env = defineEnv({ EDGE_READ: { class: "plain" } });
    let thrown: unknown;
    try {
      void env.EDGE_READ;
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(EnvEdgeError);
    const message = (thrown as Error).message;
    expect(message).toContain("EDGE_READ");
    expect(message).toContain("edge");
    expect(message).toContain("nodejs");
    // No class is deliverable to the edge, so reclassifying is not a remedy
    // and must not be offered.
    expect(message).not.toContain("reclassif");
    expect(message).not.toContain("ocel env set");
  });

  it("names the entry the shim is running when it knows one", () => {
    (globalThis as Record<string, unknown>)[ENTRY_GLOBAL] =
      "middleware_app/api/edge/route";
    const env = defineEnv({ EDGE_ENTRY_NAMED: { class: "plain" } });
    expect(() => void env.EDGE_ENTRY_NAMED).toThrow(
      /middleware_app\/api\/edge\/route/,
    );
  });

  it("throws for every class, because none is deliverable to the edge", () => {
    const env = defineEnv({
      EDGE_PLAIN: { class: "plain" },
      EDGE_SENSITIVE: { class: "sensitive" },
      EDGE_SECRET: { class: "secret" },
    });
    for (const key of ["EDGE_PLAIN", "EDGE_SENSITIVE", "EDGE_SECRET"] as const) {
      expect(() => void env[key]).toThrow(EnvEdgeError);
    }
  });

  it("throws even when the environment carries a value", () => {
    process.env.EDGE_SET = "from-the-environment";
    process.env.OCEL_VAR_EDGE_SET = "from-the-membrane";
    try {
      const env = defineEnv({ EDGE_SET: { class: "plain" } });
      expect(() => void env.EDGE_SET).toThrow(EnvEdgeError);
    } finally {
      delete process.env.EDGE_SET;
      delete process.env.OCEL_VAR_EDGE_SET;
    }
  });
});

describe("the edge build's surface", () => {
  it("exports the same error types the node build does", async () => {
    const node = await import("./index.js");
    expect(EnvDefinitionError).toBe(node.EnvDefinitionError);
    expect(EnvScopeError).toBe(node.EnvScopeError);
    expect(EnvValueError).toBe(node.EnvValueError);
    expect(node.EnvEdgeError).toBe(EnvEdgeError);
  });

  // The `types` condition is not split: every tier is typed from
  // index.d.ts, on the argument that the two surfaces are identical. Nothing
  // was checking that they stayed identical, and the failure mode is quiet in
  // the worst direction — an export added to the node build alone is typed for
  // an edge consumer and is `undefined` at runtime there, with the compiler
  // saying nothing. This is what makes that a test failure instead.
  it("exports exactly the names the node build does, which is what lets one types entry serve both", async () => {
    const node = await import("./index.js");
    const edge = await import("./edge.js");

    expect(Object.keys(edge).sort()).toEqual(Object.keys(node).sort());
  });

  it("is wired to the edge conditions ahead of import", async () => {
    const manifest = JSON.parse(
      await readFile(join(here, "../../package.json"), "utf8"),
    );
    const entry = manifest.exports["./env"];
    const keys = Object.keys(entry);
    for (const condition of ["edge-light", "workerd", "worker"]) {
      expect(entry[condition]).toBe("./dist/env/edge.js");
      expect(keys.indexOf(condition)).toBeLessThan(keys.indexOf("import"));
    }
    expect(entry.import).toBe("./dist/env/index.js");
  });
});

// The whole fix is that `@connectrpc/connect-node` leaves the edge module
// graph: `env/index.ts` -> `declare.ts` -> `utils/rpc.ts` evaluates a Node
// transport at module scope, which is what makes an edge compile die on
// `node:http2` today. Anyone re-adding such an import to `edge.ts` — or to
// anything it reaches — silently restores that break with every other test
// still green, so the graph itself is what is pinned here.
describe("the edge module graph", () => {
  it("reaches no node builtin and no package, only relative modules", async () => {
    const visited = new Set<string>();
    const offences: string[] = [];

    async function walk(file: string): Promise<void> {
      if (visited.has(file)) return;
      visited.add(file);
      const source = await readFile(file, "utf8");
      for (const match of source.matchAll(
        /^[ \t]*(?:import|export)([\s\S]*?)from\s*["']([^"']+)["']/gm,
      )) {
        const clause = match[1]!;
        const specifier = match[2]!;
        if (/^\s*type\b/.test(clause)) continue;
        if (specifier.startsWith(".")) {
          await walk(
            resolvePath(dirname(file), specifier.replace(/\.js$/, ".ts")),
          );
          continue;
        }
        offences.push(`${file} imports '${specifier}'`);
      }
      for (const [, specifier] of source.matchAll(
        /^\s*import\s*["']([^"']+)["']/gm,
      )) {
        offences.push(`${file} side-effect imports '${specifier}'`);
      }
    }

    await walk(join(here, "edge.ts"));

    expect(offences).toEqual([]);
    expect([...visited].map((f) => f.slice(here.length + 1)).sort()).not.toContain(
      "declare.ts",
    );
  });
});
