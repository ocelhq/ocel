import { beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

const declareEnvMock = vi.hoisted(() =>
  vi.fn(() => Promise.resolve({ cells: [] as unknown[] })),
);
const reportEnvProblemsMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

// callSite names the file a declaration was written in, which is the identity
// a key is owned by. Overriding it is how one test file stands in for two
// definition files, and for one definition file running twice.
const source = vi.hoisted(() => ({ override: undefined as string | undefined }));

vi.mock("./scope.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./scope.js")>();
  return { ...actual, callSite: () => source.override ?? actual.callSite() };
});

vi.mock("../utils/rpc", () => ({
  rpc: {
    resource: {
      declare: vi.fn(),
      declareEnv: declareEnvMock,
      reportEnvProblems: reportEnvProblemsMock,
    },
  },
  ResourceType: { POSTGRES: 1, BUCKET: 2 },
}));

const { defineEnv, EnvDefinitionError, EnvScopeError, EnvValueError } =
  await import("./index.js");

// The declaration RPC is deferred like every other Ocel declaration, so a test
// drives it the way the generated discovery entrypoint does.
async function flushDeclarations() {
  const pending = globalThis.__ocelRegister ?? [];
  globalThis.__ocelRegister = [];
  await Promise.all(pending);
}

function cell(key: string, value: string, folder = "") {
  return { key, folder, value };
}

interface Problem {
  key: string;
  folder: string;
  kind: number;
  detail: string;
}

function reportedProblems(): Problem[] {
  const [call] = reportEnvProblemsMock.mock.calls as unknown as [
    [{ problems: Problem[] }],
  ];
  return call![0].problems;
}

// A schema whose complaint quotes the value it rejected. Zod's own messages do
// this and a custom message may do anything, so it is the shape the redaction
// has to survive rather than an exotic one.
function echoingSchema() {
  return z.string().refine(() => false, {
    error: (issue) => `received ${issue.input}`,
  });
}

beforeEach(() => {
  declareEnvMock.mockClear();
  reportEnvProblemsMock.mockClear();
  declareEnvMock.mockResolvedValue({ cells: [] });
  globalThis.__ocelRegister = [];
  source.override = undefined;
});

describe("definition errors", () => {
  it("rejects a key another definitions file already declared", () => {
    source.override = "/project/env.first.ts";
    defineEnv({ DUPE_KEY: { class: "plain" } });

    source.override = "/project/env.second.ts";
    expect(() => defineEnv({ DUPE_KEY: { class: "secret" } })).toThrow(
      EnvDefinitionError,
    );
    expect(() => defineEnv({ DUPE_KEY: { class: "secret" } })).toThrow(
      /DUPE_KEY/,
    );
    expect(() => defineEnv({ DUPE_KEY: { class: "secret" } })).toThrow(
      /env\.first\.ts/,
    );
  });

  // A definitions file is re-executed on every reload under `ocel dev`, and a
  // file re-stating what it already declared is that reload, not a second
  // claim on the key.
  it("lets the file that declared a key declare it again", () => {
    source.override = "/project/env.reloaded.ts";
    defineEnv({ RELOADED_KEY: { class: "plain" } });

    expect(() => defineEnv({ RELOADED_KEY: { class: "sensitive" } })).not.toThrow();
  });

  it("rejects a platform-owned name for a bare-key class", () => {
    expect(() => defineEnv({ AWS_REGION: { class: "plain" } })).toThrow(
      /reserved/i,
    );
    expect(() => defineEnv({ OCEL_THING: { class: "plain" } })).toThrow(
      /reserved/i,
    );
    expect(() =>
      defineEnv({ NEXT_PUBLIC_ID: { class: "plain", client: true } }),
    ).toThrow(/reserved/i);
  });

  it("allows a platform-owned name for a class that is never delivered bare", () => {
    expect(() => defineEnv({ AWS_ROTATION_TOKEN: { class: "secret" } })).not.toThrow();
  });

  it("rejects a name that is not a usable environment variable name", () => {
    expect(() => defineEnv({ "BAD#KEY": { class: "plain" } })).toThrow(
      EnvDefinitionError,
    );
    expect(() => defineEnv({ "lower-case": { class: "plain" } })).toThrow(
      EnvDefinitionError,
    );
  });

  // The type of a definition refuses this pairing outright — env.test-d.ts
  // holds that proof — so what is left to check here is the caller the
  // compiler never sees, since the union is erased by the time this runs.
  it("rejects client access on an encrypted class from an untyped caller", () => {
    expect(() =>
      // @ts-expect-error the pairing this asserts on does not typecheck
      defineEnv({ CLIENT_SENSITIVE: { class: "sensitive", client: true } }),
    ).toThrow(/client/i);
    expect(() =>
      // @ts-expect-error the pairing this asserts on does not typecheck
      defineEnv({ CLIENT_SECRET_KEY: { class: "secret", client: true } }),
    ).toThrow(/client/i);
  });

  it("rejects a schema default on a live class", () => {
    expect(() =>
      defineEnv({
        LIVE_DEFAULTED: { class: "secret", schema: z.string().default("x") },
      }),
    ).toThrow(/default/i);
    expect(() =>
      defineEnv({
        BAKED_DEFAULTED: { class: "plain", schema: z.string().default("x") },
      }),
    ).not.toThrow();
  });

});

describe("the declaration payload", () => {
  it("carries every variable of one call, with its class and whether it is required", async () => {
    defineEnv({
      PAYLOAD_PLAIN: { class: "plain", client: true },
      PAYLOAD_SECRET: { class: "secret" },
      PAYLOAD_DEFAULTED: { class: "sensitive", schema: z.string().default("d") },
    });
    await flushDeclarations();

    expect(declareEnvMock).toHaveBeenCalledTimes(1);
    const source = expect.stringContaining("env.test.ts");
    expect(declareEnvMock).toHaveBeenCalledWith({
      definitions: [
        {
          key: "PAYLOAD_PLAIN",
          class: 1,
          clientAccessible: true,
          required: true,
          folders: [],
          source,
        },
        {
          key: "PAYLOAD_SECRET",
          class: 3,
          clientAccessible: false,
          required: true,
          folders: [],
          source,
        },
        {
          key: "PAYLOAD_DEFAULTED",
          class: 2,
          clientAccessible: false,
          required: false,
          folders: [],
          source,
        },
      ],
    });
  });
});

describe("validation against the stored cells", () => {
  it("reports a required key the store has no cell for", async () => {
    defineEnv({ VALIDATE_MISSING: { class: "plain" } });
    await flushDeclarations();

    expect(reportEnvProblemsMock).toHaveBeenCalledWith({
      problems: [{ key: "VALIDATE_MISSING", folder: "", kind: 1, detail: "" }],
    });
  });

  it("reports a present value its schema rejects, naming what failed", async () => {
    declareEnvMock.mockResolvedValue({ cells: [cell("VALIDATE_SHORT", "")] });
    defineEnv({
      VALIDATE_SHORT: { class: "plain", schema: z.string().min(1) },
    });
    await flushDeclarations();

    const [call] = reportEnvProblemsMock.mock.calls as unknown as [
      [{ problems: { key: string; kind: number; detail: string }[] }],
    ];
    expect(call![0].problems).toHaveLength(1);
    expect(call![0].problems[0]!.key).toBe("VALIDATE_SHORT");
    expect(call![0].problems[0]!.kind).toBe(2);
    expect(call![0].problems[0]!.detail).not.toBe("");
  });

  it("validates every cell a key has, including a folder no app binds", async () => {
    declareEnvMock.mockResolvedValue({
      cells: [
        cell("VALIDATE_FOLDERS", "https://ok.example.com"),
        cell("VALIDATE_FOLDERS", "not-a-url", "/checkout"),
      ],
    });
    defineEnv({ VALIDATE_FOLDERS: { class: "plain", schema: z.url() } });
    await flushDeclarations();

    const [call] = reportEnvProblemsMock.mock.calls as unknown as [
      [{ problems: { folder: string }[] }],
    ];
    expect(call![0].problems).toHaveLength(1);
    expect(call![0].problems[0]!.folder).toBe("/checkout");
  });

  it("says nothing when every cell satisfies its schema", async () => {
    declareEnvMock.mockResolvedValue({ cells: [cell("VALIDATE_OK", "yes")] });
    defineEnv({ VALIDATE_OK: { class: "plain", schema: z.string().min(1) } });
    await flushDeclarations();

    expect(reportEnvProblemsMock).not.toHaveBeenCalled();
  });

  it("does not require a key whose schema supplies a default", async () => {
    defineEnv({
      VALIDATE_DEFAULTED: { class: "plain", schema: z.string().default("d") },
    });
    await flushDeclarations();

    expect(reportEnvProblemsMock).not.toHaveBeenCalled();
  });

  it("takes a live-class cell as present without a value, and never schema-checks it", async () => {
    declareEnvMock.mockResolvedValue({
      cells: [cell("LIVE_PRESENT", "")],
    });
    defineEnv({
      LIVE_PRESENT: { class: "secret", schema: z.string().min(1) },
      LIVE_ABSENT: { class: "secret" },
    });
    await flushDeclarations();

    expect(reportEnvProblemsMock).toHaveBeenCalledWith({
      problems: [{ key: "LIVE_ABSENT", folder: "", kind: 1, detail: "" }],
    });
  });
});

// A schema's complaint is written by the developer and computed from the value
// it rejected, so it is not safe to forward for a class whose values are
// confidential. What survives is the fact of the failure and the cell it
// happened in; the reason survives only for the plaintext class.
describe("the confidentiality of a schema's complaint", () => {
  const SECRET_VALUE = "sk_live_super_secret";

  it("echoes the value it rejected, which is what makes this worth guarding", () => {
    const result = echoingSchema()["~standard"].validate(SECRET_VALUE);
    expect(result).not.toBeInstanceOf(Promise);
    expect(JSON.stringify(result)).toContain(SECRET_VALUE);
  });

  it("keeps an encrypted value out of the problem it reports", async () => {
    declareEnvMock.mockResolvedValue({
      cells: [cell("REDACT_SENSITIVE", SECRET_VALUE)],
    });
    defineEnv({ REDACT_SENSITIVE: { class: "sensitive", schema: echoingSchema() } });
    await flushDeclarations();

    expect(JSON.stringify(reportEnvProblemsMock.mock.calls)).not.toContain(
      SECRET_VALUE,
    );
    const [problem] = reportedProblems();
    expect(problem!.key).toBe("REDACT_SENSITIVE");
    expect(problem!.kind).toBe(2);
    expect(problem!.detail).not.toBe("");
  });

  it("keeps the schema's own words for a plaintext value, which is not confidential", async () => {
    declareEnvMock.mockResolvedValue({
      cells: [cell("REDACT_PLAIN", "pk_test_123")],
    });
    defineEnv({ REDACT_PLAIN: { class: "plain", schema: echoingSchema() } });
    await flushDeclarations();

    expect(reportedProblems()[0]!.detail).toContain("received pk_test_123");
  });

  it("keeps an encrypted value out of the error a read throws", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("OCEL_VAR_REDACT_READ_SEALED", SECRET_VALUE);
    const env = defineEnv({
      REDACT_READ_SEALED: { class: "sensitive", schema: echoingSchema() },
    });

    expect(() => env.REDACT_READ_SEALED).toThrow(EnvValueError);
    let thrown = "";
    try {
      env.REDACT_READ_SEALED;
    } catch (error) {
      thrown = String(error);
    }
    expect(thrown).not.toContain(SECRET_VALUE);
    expect(thrown).toContain("REDACT_READ_SEALED");
  });

  it("keeps the schema's own words in the error a plaintext read throws", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("REDACT_READ_PLAIN", "pk_test_456");
    const env = defineEnv({
      REDACT_READ_PLAIN: { class: "plain", schema: echoingSchema() },
    });

    expect(() => env.REDACT_READ_PLAIN).toThrow(/received pk_test_456/);
  });
});

describe("reading a variable", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  it("reads as a plain synchronous property", () => {
    vi.stubEnv("READ_PLAIN", "hello");
    const env = defineEnv({ READ_PLAIN: { class: "plain" } });
    expect(env.READ_PLAIN).toBe("hello");
  });

  // Interop is what distinguishes the plaintext class: a third-party library
  // that reads the process environment itself has to find the value under the
  // name the user chose, which is why it is delivered bare rather than
  // namespaced.
  it("gives a plaintext value the same answer through the object and the process environment", () => {
    vi.stubEnv("READ_INTEROP", "pk_live_123");
    const env = defineEnv({ READ_INTEROP: { class: "plain" } });
    expect(env.READ_INTEROP).toBe("pk_live_123");
    expect(process.env.READ_INTEROP).toBe("pk_live_123");
  });

  // An encrypted-baked value never reaches the process environment under the
  // key the user chose — the membrane injects it namespaced, after opening the
  // bundle it rode in. Reading it is nonetheless the same property access as
  // any other class, which is what lets a key be reclassified without a call
  // site changing.
  it("reads an encrypted-baked value the membrane injected under its namespaced name", () => {
    vi.stubEnv("OCEL_VAR_READ_SEALED", "sk_live_123");
    const env = defineEnv({ READ_SEALED: { class: "sensitive" } });
    expect(env.READ_SEALED).toBe("sk_live_123");
    expect(process.env.READ_SEALED).toBeUndefined();
  });

  // A value delivered namespaced was delivered that way because it is not
  // meant to be readable from the process environment under its own name. If
  // something else set that name anyway, the sealed value is still the answer.
  it("prefers the namespaced value over a bare name of the same key", () => {
    vi.stubEnv("OCEL_VAR_READ_SHADOWED", "sealed");
    vi.stubEnv("READ_SHADOWED", "impostor");
    const env = defineEnv({ READ_SHADOWED: { class: "sensitive" } });
    expect(env.READ_SHADOWED).toBe("sealed");
  });

  it("parses through the schema, so the property is the schema's output", () => {
    vi.stubEnv("READ_PARSED", "8080");
    const env = defineEnv({
      READ_PARSED: { class: "plain", schema: z.coerce.number() },
    });
    expect(env.READ_PARSED).toBe(8080);
  });

  it("falls back to the schema's default and nowhere else", () => {
    const env = defineEnv({
      READ_DEFAULTED: { class: "plain", schema: z.string().default("fallback") },
    });
    expect(env.READ_DEFAULTED).toBe("fallback");
  });

  it("throws a named error, naming the fixing command, when nothing is set", () => {
    const env = defineEnv({ READ_UNSET: { class: "plain" } });
    expect(() => env.READ_UNSET).toThrow(EnvValueError);
    expect(() => env.READ_UNSET).toThrow(/ocel env set READ_UNSET/);
  });

  it("throws a named error when the value is present but out of schema", () => {
    vi.stubEnv("READ_BAD", "nope");
    const env = defineEnv({
      READ_BAD: { class: "plain", schema: z.string().email() },
    });
    expect(() => env.READ_BAD).toThrow(EnvValueError);
  });

  it("refuses a schema that cannot answer synchronously", () => {
    vi.stubEnv("READ_ASYNC", "anything");
    const env = defineEnv({
      READ_ASYNC: { class: "plain", schema: z.string().refine(async () => true) },
    });
    expect(() => env.READ_ASYNC).toThrow(/synchronous/i);
  });

  it("refuses to be read during discovery, when no value has been resolved yet", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");
    vi.stubEnv("READ_IN_DISCOVERY", "set-but-irrelevant");
    const env = defineEnv({ READ_IN_DISCOVERY: { class: "plain" } });
    expect(() => env.READ_IN_DISCOVERY).toThrow(/discovery/);
  });
});

describe("folder scoping", () => {
  it.each([
    ["SCOPE_BAD_RELATIVE", "web", /must start with/],
    ["SCOPE_BAD_TRAILING", "/web/", /must not end with/],
    ["SCOPE_BAD_EMPTY_SEGMENT", "/web//admin", /empty segments/],
    ["SCOPE_BAD_DELIMITER", "/we#b", /may not contain '#'/],
    ["SCOPE_BAD_ROOT", "/", /project root/],
  ])("rejects %s, scoped to a folder the store cannot address", (key, folder, message) => {
    expect(() => defineEnv({ [key]: { class: "plain", folders: [folder] } })).toThrow(
      EnvDefinitionError,
    );
    expect(() => defineEnv({ [key]: { class: "plain", folders: [folder] } })).toThrow(
      message,
    );
  });

  it("rejects a scope that names one folder twice", () => {
    expect(() =>
      defineEnv({ SCOPE_DUPE: { class: "plain", folders: ["/web", "/web"] } }),
    ).toThrow(/\/web/);
  });

  it("rejects an empty scope, which would silently mean unscoped", () => {
    expect(() =>
      defineEnv({ SCOPE_EMPTY: { class: "plain", folders: [] } }),
    ).toThrow(EnvDefinitionError);
  });

  it("carries the scope on the declaration", async () => {
    defineEnv({ SCOPE_SENT: { class: "plain", folders: ["/web", "/admin"] } });
    await flushDeclarations();

    const [call] = declareEnvMock.mock.calls as unknown as [
      [{ definitions: { key: string; folders: string[] }[] }],
    ];
    expect(call![0].definitions[0]!.folders).toEqual(["/web", "/admin"]);
  });

  it("requires a scoped key in every folder it names, and never at root", async () => {
    declareEnvMock.mockResolvedValue({
      cells: [cell("SCOPE_PARTIAL", "ph_web", "/web")],
    });
    defineEnv({ SCOPE_PARTIAL: { class: "plain", folders: ["/web", "/admin"] } });
    await flushDeclarations();

    expect(reportEnvProblemsMock).toHaveBeenCalledWith({
      problems: [{ key: "SCOPE_PARTIAL", folder: "/admin", kind: 1, detail: "" }],
    });
  });

  it("does not accept a root value as covering a scoped key", async () => {
    declareEnvMock.mockResolvedValue({ cells: [cell("SCOPE_ROOTED", "one")] });
    defineEnv({ SCOPE_ROOTED: { class: "plain", folders: ["/web"] } });
    await flushDeclarations();

    expect(reportEnvProblemsMock).toHaveBeenCalledWith({
      problems: [{ key: "SCOPE_ROOTED", folder: "/web", kind: 1, detail: "" }],
    });
  });

  it("requires an unscoped key at root even when a folder overrides it", async () => {
    declareEnvMock.mockResolvedValue({
      cells: [cell("SCOPE_UNSCOPED", "override", "/web")],
    });
    defineEnv({ SCOPE_UNSCOPED: { class: "plain" } });
    await flushDeclarations();

    expect(reportEnvProblemsMock).toHaveBeenCalledWith({
      problems: [{ key: "SCOPE_UNSCOPED", folder: "", kind: 1, detail: "" }],
    });
  });

  it("says nothing when every folder a scope names is set", async () => {
    declareEnvMock.mockResolvedValue({
      cells: [
        cell("SCOPE_COMPLETE", "a", "/web"),
        cell("SCOPE_COMPLETE", "b", "/admin"),
      ],
    });
    defineEnv({ SCOPE_COMPLETE: { class: "plain", folders: ["/web", "/admin"] } });
    await flushDeclarations();

    expect(reportEnvProblemsMock).not.toHaveBeenCalled();
  });
});

describe("reading a key outside the app's scope", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  it("throws a named error stating the scope and the binding", () => {
    vi.stubEnv("OCEL_APP_FOLDER", "/admin");
    vi.stubEnv("SCOPE_READ_OUT", "leaked");
    const env = defineEnv({
      SCOPE_READ_OUT: { class: "plain", folders: ["/web"] },
    });

    expect(() => env.SCOPE_READ_OUT).toThrow(EnvScopeError);
    expect(() => env.SCOPE_READ_OUT).toThrow(/\/web/);
    expect(() => env.SCOPE_READ_OUT).toThrow(/\/admin/);
  });

  it("names the project root as the binding for an app that binds nothing", () => {
    vi.stubEnv("OCEL_APP_FOLDER", "");
    vi.stubEnv("SCOPE_READ_UNBOUND", "leaked");
    const env = defineEnv({
      SCOPE_READ_UNBOUND: { class: "plain", folders: ["/web"] },
    });

    expect(() => env.SCOPE_READ_UNBOUND).toThrow(/project root/);
  });

  it("reads normally when the app is bound inside the scope", () => {
    vi.stubEnv("OCEL_APP_FOLDER", "/web");
    vi.stubEnv("SCOPE_READ_IN", "ph_web");
    const env = defineEnv({
      SCOPE_READ_IN: { class: "plain", folders: ["/web"] },
    });

    expect(env.SCOPE_READ_IN).toBe("ph_web");
  });

  it("leaves an unscoped key readable from any binding", () => {
    vi.stubEnv("OCEL_APP_FOLDER", "/admin");
    vi.stubEnv("SCOPE_READ_ANY", "fine");
    const env = defineEnv({ SCOPE_READ_ANY: { class: "plain" } });

    expect(env.SCOPE_READ_ANY).toBe("fine");
  });
});

// The app's folder binding is the one folder-shaped thing the runtime is told,
// and it is admitted on exactly one condition: it carries no value-selection
// power. It may decide whether a scoped read fails, and it may decide nothing
// else — which value a successful read returns, and which environment name
// that value came from, are the same at every binding. These tests pin that
// condition rather than the wording of the error, so a read that ever chose a
// value by folder fails here even if every message still reads correctly.
//
// What they pin is observational, and bounded by what plant seeds: no
// folder-derived lookup name, and no hit on a folder-keyed cell under the
// spellings the store could use. A read that instead decoded a folder out of a
// structured payload inside one variable would consult identical names at
// every binding and is invisible here. No test at this layer can refute an
// encoding it did not plant; the guarantee that closes that gap is that
// delivery resolves values before the runtime sees them at all.
describe("the app folder binding selects no value", () => {
  const RESOLVED = "the one resolved value";

  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  // plant puts the resolved value where delivery puts it, and puts a different
  // value under every name a folder-aware read could address a cell by: the
  // store spells a scoped cell '<folder>#<key>', so those are the names such a
  // read would reach for. Without them a folder-selecting read would return
  // the same string by accident and prove nothing.
  function plant(key: string, folders: readonly string[]) {
    vi.stubEnv(`OCEL_VAR_${key}`, RESOLVED);
    vi.stubEnv(key, RESOLVED);
    for (const folder of folders) {
      const chosen = `a value chosen for ${folder}`;
      vi.stubEnv(`OCEL_VAR_${folder}#${key}`, chosen);
      vi.stubEnv(`${folder}#${key}`, chosen);
      vi.stubEnv(`${key}#${folder}`, chosen);
    }
  }

  // observe reports what a read returned or threw, and every environment name
  // it consulted. The names matter on their own: a read that addressed a cell
  // by folder consults a different name under a different binding even when
  // the two cells happen to hold the same string.
  function observe(read: () => unknown) {
    const real = process.env;
    const consulted: string[] = [];
    const spy = new Proxy(real, {
      get(target, property) {
        if (typeof property === "string") consulted.push(property);
        return Reflect.get(target, property);
      },
    });

    const install = (value: NodeJS.ProcessEnv) =>
      Object.defineProperty(process, "env", {
        configurable: true,
        writable: true,
        value,
      });

    install(spy);
    try {
      return { outcome: `value:${String(read())}`, consulted };
    } catch (error) {
      return { outcome: (error as Error).name, consulted };
    } finally {
      install(real);
    }
  }

  it("yields one value, from one environment name, at every binding its scope permits", () => {
    plant("SELECT_IN_SCOPE", ["/web", "/admin"]);

    const observations = ["/web", "/admin"].map((binding) => {
      vi.stubEnv("OCEL_APP_FOLDER", binding);
      const env = defineEnv({
        SELECT_IN_SCOPE: { class: "plain", folders: ["/web", "/admin"] },
      });
      return observe(() => env.SELECT_IN_SCOPE);
    });

    for (const { outcome, consulted } of observations) {
      expect(outcome).toBe(`value:${RESOLVED}`);
      expect(consulted).toEqual(observations[0]!.consulted);
      expect(consulted.filter((name) => /\/(web|admin)/.test(name))).toEqual([]);
    }
  });

  it("can only turn a read into a scope error, never into a different value", () => {
    plant("SELECT_PARTITION", ["/web", "/admin"]);

    const outcomeAt = (binding: string) => {
      vi.stubEnv("OCEL_APP_FOLDER", binding);
      const env = defineEnv({
        SELECT_PARTITION: { class: "plain", folders: ["/web", "/admin"] },
      });
      return observe(() => env.SELECT_PARTITION).outcome;
    };

    // Which binding lands on which side is the claim, so this pins the mapping
    // rather than the set of outcomes a set would let any one binding flip
    // silently. '/web/nested' carries the nesting invariant: a match that grew
    // nesting-aware would hand it a value that deploy-time resolution, which
    // matches a binding whole, never delivers — turning a build-time scope
    // error into a runtime missing value.
    const bindings = ["/web", "/admin", "", "/marketing", "/web/nested", "/WEB"];
    expect(Object.fromEntries(bindings.map((b) => [b, outcomeAt(b)]))).toEqual({
      "/web": `value:${RESOLVED}`,
      "/admin": `value:${RESOLVED}`,
      "": "EnvScopeError",
      "/marketing": "EnvScopeError",
      "/web/nested": "EnvScopeError",
      "/WEB": "EnvScopeError",
    });
  });

  it("changes nothing at all for a key no scope names", () => {
    plant("SELECT_UNSCOPED", ["/web", "/admin"]);

    const outcomes = new Set(
      ["", "/web", "/admin", "/marketing"].map((binding) => {
        vi.stubEnv("OCEL_APP_FOLDER", binding);
        const env = defineEnv({ SELECT_UNSCOPED: { class: "plain" } });
        return observe(() => env.SELECT_UNSCOPED).outcome;
      }),
    );

    expect([...outcomes]).toEqual([`value:${RESOLVED}`]);
  });
});
