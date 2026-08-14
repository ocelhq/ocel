import { beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

const declareEnvMock = vi.hoisted(() =>
  vi.fn(() => Promise.resolve({ cells: [] as unknown[] })),
);
const reportEnvProblemsMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

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
}));

const { defineEnv, EnvDefinitionError, EnvScopeError, EnvValueError } =
  await import("./index.js");

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

function echoingSchema() {
  return z.string().refine(() => false, {
    error: (issue) => `received ${issue.input}`,
  });
}

const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

function push(generation: number, values: Record<string, string>) {
  (globalThis as Record<symbol, unknown>)[LIVE_VALUES] = { generation, values };
}

beforeEach(() => {
  declareEnvMock.mockClear();
  reportEnvProblemsMock.mockClear();
  declareEnvMock.mockResolvedValue({ cells: [] });
  globalThis.__ocelRegister = [];
  source.override = undefined;
  delete (globalThis as Record<symbol, unknown>)[LIVE_VALUES];
  vi.unstubAllEnvs();
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

  it("lets the file that declared a key declare it again", () => {
    source.override = "/project/env.reloaded.ts";
    defineEnv({ RELOADED_KEY: { class: "plain" } });

    expect(() => defineEnv({ RELOADED_KEY: { class: "sensitive" } })).not.toThrow();
  });

  it("rejects an Ocel-owned name for a bare-key class", () => {
    expect(() => defineEnv({ OCEL_THING: { class: "plain" } })).toThrow(
      /reserved/i,
    );
  });

  it("allows an Ocel-owned name for a class that is never delivered bare", () => {
    expect(() => defineEnv({ OCEL_ROTATION_TOKEN: { class: "secret" } })).not.toThrow();
  });

  it("allows a name a provider or a bundler gives its own meaning", () => {
    expect(() => defineEnv({ AWS_REGION: { class: "plain" } })).not.toThrow();
    expect(() =>
      defineEnv({ NEXT_PUBLIC_ID: { class: "plain", client: true } }),
    ).not.toThrow();
    expect(() =>
      defineEnv({ VITE_ID: { class: "plain", client: true } }),
    ).not.toThrow();
  });

  it("rejects a name that is not a usable environment variable name", () => {
    expect(() => defineEnv({ "BAD#KEY": { class: "plain" } })).toThrow(
      EnvDefinitionError,
    );
    expect(() => defineEnv({ "lower-case": { class: "plain" } })).toThrow(
      EnvDefinitionError,
    );
  });

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

  it("rejects a schema that accepts a missing value on a client-accessible key", () => {
    expect(() =>
      defineEnv({
        CLIENT_DEFAULTED: {
          class: "plain",
          client: true,
          schema: z.string().default("x"),
        },
      }),
    ).toThrow(/accepts a missing value/i);
    expect(() =>
      defineEnv({
        CLIENT_OPTIONAL: {
          class: "plain",
          client: true,
          schema: z.string().optional(),
        },
      }),
    ).toThrow(/accepts a missing value/i);
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

  it("gives a plaintext value the same answer through the object and the process environment", () => {
    vi.stubEnv("READ_INTEROP", "pk_live_123");
    const env = defineEnv({ READ_INTEROP: { class: "plain" } });
    expect(env.READ_INTEROP).toBe("pk_live_123");
    expect(process.env.READ_INTEROP).toBe("pk_live_123");
  });

  it("reads an encrypted-baked value the membrane injected under its namespaced name", () => {
    vi.stubEnv("OCEL_VAR_READ_SEALED", "sk_live_123");
    const env = defineEnv({ READ_SEALED: { class: "sensitive" } });
    expect(env.READ_SEALED).toBe("sk_live_123");
    expect(process.env.READ_SEALED).toBeUndefined();
  });

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

describe("reading a live value", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  it("reads a pushed value as the same plain synchronous property as any class", () => {
    push(1, { LIVE_READ: "sk_live_pushed" });
    const env = defineEnv({ LIVE_READ: { class: "secret" } });

    expect(env.LIVE_READ).toBe("sk_live_pushed");
    expect(process.env.LIVE_READ).toBeUndefined();
    expect(process.env.OCEL_VAR_LIVE_READ).toBeUndefined();
  });

  it("answers the same call site whatever the class delivering it", () => {
    vi.stubEnv("RECLASSIFIED", "the value");
    vi.stubEnv("OCEL_VAR_RECLASSIFIED", "the value");
    push(1, { RECLASSIFIED: "the value" });

    const read = (env: { RECLASSIFIED: string }) => env.RECLASSIFIED;

    source.override = "/project/env.plain.ts";
    expect(read(defineEnv({ RECLASSIFIED: { class: "plain" } }))).toBe("the value");
    source.override = "/project/env.plain.ts";
    expect(read(defineEnv({ RECLASSIFIED: { class: "sensitive" } }))).toBe("the value");
    source.override = "/project/env.plain.ts";
    expect(read(defineEnv({ RECLASSIFIED: { class: "secret" } }))).toBe("the value");
  });

  it("resolves a read written at module scope, which runs the moment the file is imported", () => {
    push(1, { LIVE_AT_IMPORT: "8080" });
    const port = defineEnv({
      LIVE_AT_IMPORT: { class: "secret", schema: z.coerce.number() },
    }).LIVE_AT_IMPORT;

    expect(port).toBe(8080);
  });

  it("serves a rotated value once a later generation arrives", () => {
    push(1, { LIVE_ROTATED: "before" });
    const env = defineEnv({ LIVE_ROTATED: { class: "secret" } });
    expect(env.LIVE_ROTATED).toBe("before");

    push(2, { LIVE_ROTATED: "after" });
    expect(env.LIVE_ROTATED).toBe("after");
  });

  it("resolves once within a generation, so a read costs nothing after the first", () => {
    let parses = 0;
    const counting = z.string().transform((value) => {
      parses += 1;
      return value;
    });
    push(1, { LIVE_MEMOISED: "v1" });
    const env = defineEnv({ LIVE_MEMOISED: { class: "secret", schema: counting } });

    expect(env.LIVE_MEMOISED).toBe("v1");
    expect(env.LIVE_MEMOISED).toBe("v1");
    expect(parses).toBe(1);

    push(2, { LIVE_MEMOISED: "v2" });
    expect(env.LIVE_MEMOISED).toBe("v2");
    expect(parses).toBe(2);
  });

  it("does not re-read within a generation, so the generation is what invalidates it", () => {
    push(1, { LIVE_PINNED: "first" });
    const env = defineEnv({ LIVE_PINNED: { class: "secret" } });
    expect(env.LIVE_PINNED).toBe("first");

    push(1, { LIVE_PINNED: "swapped underneath" });
    expect(env.LIVE_PINNED).toBe("first");
  });

  it("does not take a published entry that is not a string as a value", () => {
    push(1, { LIVE_NOT_A_STRING: 42 as unknown as string });

    expect(() => defineEnv({ LIVE_NOT_A_STRING: { class: "secret" } })).toThrow(
      EnvValueError,
    );
  });

  it("fails init loudly when the push carried no value for a declared key", () => {
    push(1, { SOMETHING_ELSE: "x" });

    expect(() => defineEnv({ LIVE_ABSENT_FROM_PUSH: { class: "secret" } })).toThrow(
      EnvValueError,
    );
    expect(() => defineEnv({ LIVE_ABSENT_FROM_PUSH: { class: "secret" } })).toThrow(
      /ocel env set LIVE_ABSENT_FROM_PUSH/,
    );
  });
});

describe("a live value is checked against its schema at init", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  it("throws where the variable is declared, not where it is read", () => {
    push(1, { LIVE_DRIFTED: "not-a-url" });

    expect(() => defineEnv({ LIVE_DRIFTED: { class: "secret", schema: z.url() } })).toThrow(
      EnvValueError,
    );
  });

  it("keeps the value out of the failure, as every confidential class does", () => {
    const drifted = "sk_live_drifted_value";
    push(1, { LIVE_DRIFT_REDACTED: drifted });

    let thrown = "";
    try {
      defineEnv({ LIVE_DRIFT_REDACTED: { class: "secret", schema: echoingSchema() } });
    } catch (error) {
      thrown = String(error);
    }
    expect(thrown).toContain("LIVE_DRIFT_REDACTED");
    expect(thrown).not.toContain(drifted);
  });

  it("checks only live keys, leaving every other class to its first read", () => {
    push(1, {});

    expect(() =>
      defineEnv({ INIT_UNSET_PLAIN: { class: "plain", schema: z.url() } }),
    ).not.toThrow();
  });

  it("leaves a key this app's binding puts out of scope alone", () => {
    vi.stubEnv("OCEL_APP_FOLDER", "/admin");
    push(1, {});

    expect(() =>
      defineEnv({ LIVE_OTHER_FOLDER: { class: "secret", folders: ["/web"] } }),
    ).not.toThrow();
  });

  it("says nothing during discovery, when no value has been resolved yet", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");
    push(1, { LIVE_IN_DISCOVERY: "not-a-url" });

    expect(() =>
      defineEnv({ LIVE_IN_DISCOVERY: { class: "secret", schema: z.url() } }),
    ).not.toThrow();
  });
});

describe("a function that declares no live value", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  it("resolves every other class with no live values published at all", () => {
    vi.stubEnv("OUTAGE_PLAIN", "still here");
    vi.stubEnv("OCEL_VAR_OUTAGE_SEALED", "still sealed");

    const env = defineEnv({
      OUTAGE_PLAIN: { class: "plain" },
      OUTAGE_SEALED: { class: "sensitive" },
    });

    expect((globalThis as Record<symbol, unknown>)[LIVE_VALUES]).toBeUndefined();
    expect(env.OUTAGE_PLAIN).toBe("still here");
    expect(env.OUTAGE_SEALED).toBe("still sealed");
  });

  it("leaves a live key to its first read when nothing was pushed", () => {
    expect(() => defineEnv({ LIVE_NO_PUSH: { class: "secret" } })).not.toThrow();
  });

  it("falls through to the environment for a live key when nothing was pushed", () => {
    vi.stubEnv("OCEL_VAR_LIVE_IN_DEV", "sk_dev_123");
    const env = defineEnv({ LIVE_IN_DEV: { class: "secret" } });

    expect(env.LIVE_IN_DEV).toBe("sk_dev_123");
  });

  it("falls through to the bare name dev delivers a live value under", () => {
    vi.stubEnv("LIVE_IN_DEV_BARE", "sk_dev_bare");
    const env = defineEnv({ LIVE_IN_DEV_BARE: { class: "secret" } });

    expect(env.LIVE_IN_DEV_BARE).toBe("sk_dev_bare");
  });

  it("prefers a pushed value to one standing under the same name in the environment", () => {
    vi.stubEnv("LIVE_PUSH_WINS", "from_the_environment");
    push(1, { LIVE_PUSH_WINS: "from_the_push" });
    const env = defineEnv({ LIVE_PUSH_WINS: { class: "secret" } });

    expect(env.LIVE_PUSH_WINS).toBe("from_the_push");
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

describe("the app folder binding selects no value", () => {
  const RESOLVED = "the one resolved value";

  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

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

  it("selects a live value by bare key alone, at every binding its scope permits", () => {
    plant("SELECT_LIVE", ["/web", "/admin"]);
    push(1, {
      SELECT_LIVE: "the one pushed value",
      "/web#SELECT_LIVE": "a value chosen for /web",
      "/admin#SELECT_LIVE": "a value chosen for /admin",
      "SELECT_LIVE#/web": "a value chosen for /web",
      "SELECT_LIVE#/admin": "a value chosen for /admin",
    });

    const observations = ["/web", "/admin"].map((binding) => {
      vi.stubEnv("OCEL_APP_FOLDER", binding);
      const env = defineEnv({
        SELECT_LIVE: { class: "secret", folders: ["/web", "/admin"] },
      });
      return observe(() => env.SELECT_LIVE);
    });

    for (const { outcome, consulted } of observations) {
      expect(outcome).toBe("value:the one pushed value");
      expect(consulted).toEqual(observations[0]!.consulted);
      expect(consulted.filter((name) => /\/(web|admin)/.test(name))).toEqual([]);
    }
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

describe("a scoped read under `ocel dev`", () => {
  function devChildEnv(binding: string, values: Record<string, string>) {
    for (const [key, value] of Object.entries(values)) vi.stubEnv(key, value);
    vi.stubEnv("OCEL_APP_FOLDER", binding);
  }

  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  it("resolves a scoped plain value for the app dev bound", () => {
    devChildEnv("/web", { DEV_SCOPED_PLAIN: "http://localhost:3000" });

    const env = defineEnv({
      DEV_SCOPED_PLAIN: { class: "plain", folders: ["/web"] },
    });

    expect(env.DEV_SCOPED_PLAIN).toBe("http://localhost:3000");
  });

  it("resolves a scoped live value through dev's environment delivery", () => {
    devChildEnv("/web", { DEV_SCOPED_LIVE: "sk_dev" });

    const env = defineEnv({
      DEV_SCOPED_LIVE: { class: "secret", folders: ["/web"] },
    });

    expect(env.DEV_SCOPED_LIVE).toBe("sk_dev");
  });

  it("reads the one broadcast value from either folder the scope names", () => {
    const read = (binding: string) => {
      devChildEnv(binding, { DEV_BROADCAST: "one value everywhere" });
      return defineEnv({
        DEV_BROADCAST: { class: "plain", folders: ["/web", "/admin"] },
      }).DEV_BROADCAST;
    };

    expect([read("/web"), read("/admin")]).toEqual([
      "one value everywhere",
      "one value everywhere",
    ]);
  });

  it("still refuses a scoped read when dev could only state the project root", () => {
    devChildEnv("", { DEV_TWO_APPS: "delivered but not this app's" });

    const env = defineEnv({
      DEV_TWO_APPS: { class: "plain", folders: ["/web"] },
    });

    expect(() => env.DEV_TWO_APPS).toThrow(EnvScopeError);
    expect(process.env.DEV_TWO_APPS).toBe("delivered but not this app's");
  });

  it("honours the empty binding dev wrote over a stale one from the shell", () => {
    vi.stubEnv("OCEL_APP_FOLDER", "/web");
    devChildEnv("", { DEV_STALE_BINDING: "not this app's either" });

    const env = defineEnv({
      DEV_STALE_BINDING: { class: "plain", folders: ["/web"] },
    });

    expect(() => env.DEV_STALE_BINDING).toThrow(/project root/);
  });
});
