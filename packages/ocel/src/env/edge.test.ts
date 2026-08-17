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
  const written: string[] = [];

  function write(key: string, value: string): void {
    process.env[key] = value;
    written.push(key);
  }

  afterEach(() => {
    for (const key of written.splice(0)) delete process.env[key];
  });

  it("reads a plain value the same way the node build does", () => {
    write("EDGE_PLAIN", "bare");
    const env = defineEnv({ EDGE_PLAIN: { class: "plain" } });
    expect(env.EDGE_PLAIN).toBe("bare");
  });

  it("prefers the membrane's name over the bare one, for both readable classes", () => {
    write("EDGE_PLAIN_BOTH", "bare");
    write("OCEL_VAR_EDGE_PLAIN_BOTH", "delivered");
    write("EDGE_SENSITIVE_BOTH", "bare");
    write("OCEL_VAR_EDGE_SENSITIVE_BOTH", "delivered");

    const env = defineEnv({
      EDGE_PLAIN_BOTH: { class: "plain" },
      EDGE_SENSITIVE_BOTH: { class: "sensitive" },
    });

    expect(env.EDGE_PLAIN_BOTH).toBe("delivered");
    expect(env.EDGE_SENSITIVE_BOTH).toBe("delivered");
  });

  it("reads a sensitive value, which arrives unsealed under the membrane's name", () => {
    write("OCEL_VAR_EDGE_SENSITIVE", "unsealed");
    const env = defineEnv({
      EDGE_SENSITIVE: { class: "sensitive", schema: z.string() },
    });
    expect(env.EDGE_SENSITIVE).toBe("unsealed");
  });

  it("throws for a key with no delivered value, as the node build does", () => {
    const env = defineEnv({ EDGE_UNSET: { class: "plain" } });
    expect(() => void env.EDGE_UNSET).toThrow(EnvValueError);
  });

  it("throws for a key no defineEnv call declares", () => {
    const env = defineEnv({ EDGE_DECLARED: { class: "plain" } }) as Record<
      string,
      unknown
    >;
    expect(() => void env.EDGE_UNDECLARED).toThrow(EnvValueError);
  });

  it("refuses a secret, naming the key, its class and the two remedies", () => {
    write("OCEL_VAR_EDGE_SECRET", "would-be-stale");
    const env = defineEnv({ EDGE_SECRET: { class: "secret" } });

    let thrown: unknown;
    try {
      void env.EDGE_SECRET;
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(EnvEdgeError);
    const message = (thrown as Error).message;
    expect(message).toContain("EDGE_SECRET");
    expect(message).toContain("secret");
    expect(message).toContain("nodejs");
    expect(message).toContain("sensitive");
  });

  it("names the entry the shim is running when it knows one", () => {
    (globalThis as Record<string, unknown>)[ENTRY_GLOBAL] =
      "middleware_app/api/edge/route";
    const env = defineEnv({ EDGE_ENTRY_NAMED: { class: "secret" } });
    expect(() => void env.EDGE_ENTRY_NAMED).toThrow(
      /middleware_app\/api\/edge\/route/,
    );
  });

  it("asserts the folder scope against the app's binding", () => {
    write("OCEL_VAR_EDGE_SCOPED", "for-web");
    const env = defineEnv({
      EDGE_SCOPED: { class: "plain", folders: ["/web"] },
    });

    write("OCEL_APP_FOLDER", "/admin");
    expect(() => void env.EDGE_SCOPED).toThrow(EnvScopeError);

    process.env.OCEL_APP_FOLDER = "/web";
    expect(env.EDGE_SCOPED).toBe("for-web");
  });

  it("asserts the folder scope before the class, as the node build does", () => {
    write("OCEL_APP_FOLDER", "/admin");
    const env = defineEnv({
      EDGE_SCOPED_SECRET: { class: "secret", folders: ["/web"] },
    });
    expect(() => void env.EDGE_SCOPED_SECRET).toThrow(EnvScopeError);
  });

  it("reads a value once and answers every later read from the memo", () => {
    write("OCEL_VAR_EDGE_MEMO", "first");
    const env = defineEnv({ EDGE_MEMO: { class: "plain" } });
    expect(env.EDGE_MEMO).toBe("first");

    process.env.OCEL_VAR_EDGE_MEMO = "second";
    expect(env.EDGE_MEMO).toBe("first");
  });

  it("does not memoize a read that threw, so a fixed value is seen", () => {
    const env = defineEnv({ EDGE_MEMO_UNSET: { class: "plain" } });
    expect(() => void env.EDGE_MEMO_UNSET).toThrow(EnvValueError);

    write("OCEL_VAR_EDGE_MEMO_UNSET", "set-late");
    expect(env.EDGE_MEMO_UNSET).toBe("set-late");
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
