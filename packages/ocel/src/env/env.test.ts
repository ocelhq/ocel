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

// LIVE_VALUES is the well-known global the entrypoint publishes a pushed
// generation of live values under. The tests write it directly because it is
// the contract between the layer's entrypoint and this SDK, not a helper: the
// two ship in different module graphs and share nothing else.
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
  // A stubbed variable outlives the test that stubbed it, so the phase a test
  // runs in would otherwise be whatever the test before it left behind.
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

// A live value is the one class that is not in the artifact and not in the
// process environment: the membrane fetches it at runtime and pushes it into
// this process, and the entrypoint publishes what arrived. What these pin is
// that arriving that way changes nothing a call site can see, and that a value
// replaced under a running process is actually observed.
describe("reading a live value", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  it("reads a pushed value as the same plain synchronous property as any class", () => {
    push(1, { LIVE_READ: "sk_live_pushed" });
    const env = defineEnv({ LIVE_READ: { class: "secret" } });

    expect(env.LIVE_READ).toBe("sk_live_pushed");
    // The push is the only delivery: nothing put it in the environment under
    // either the bare or the namespaced name.
    expect(process.env.LIVE_READ).toBeUndefined();
    expect(process.env.OCEL_VAR_LIVE_READ).toBeUndefined();
  });

  // Reclassifying is the claim the whole class scheme rests on, so it is read
  // through one expression written once against three classes.
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
    // Exactly what a module-scope `const port = env.LIVE_AT_IMPORT` does: the
    // declaration and the read are one expression, with nothing between them.
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

  // The other half of the same memo: within one generation the value is
  // resolved once and its schema run once, so a read stays a property access.
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

  // Memoising a live value forever is what a class-blind memo does, and it is
  // invisible unless something changes the values a generation holds without
  // announcing a new one. Nothing does that in production; this stands in for
  // it so the memo is pinned to the generation rather than to the first read.
  it("does not re-read within a generation, so the generation is what invalidates it", () => {
    push(1, { LIVE_PINNED: "first" });
    const env = defineEnv({ LIVE_PINNED: { class: "secret" } });
    expect(env.LIVE_PINNED).toBe("first");

    push(1, { LIVE_PINNED: "swapped underneath" });
    expect(env.LIVE_PINNED).toBe("first");
  });

  // The published map is a plain global anything in the process can reach, so
  // what it holds is not automatically a value. Serving a non-string would put
  // one in front of a schema that was promised a string.
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

// A live value is fetched after this process started, so it is the only class
// whose value can have drifted from its schema since the deploy that shipped
// the code reading it. Checking it at the declaration is what turns that drift
// into an init failure, before the app serves anything, rather than a throw in
// the middle of a request that happened to read it first.
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

// The live path is inert for a function that declares nothing live. That is
// what makes a store outage its problem and no one else's: with no push there
// is nothing to wait for, nothing to check and nothing to fail.
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

  // With no push there is nothing that could have drifted, so there is nothing
  // to check at init either. Checking anyway would turn every environment
  // without a membrane — `ocel dev`, a test, a script — into a boot failure.
  it("leaves a live key to its first read when nothing was pushed", () => {
    expect(() => defineEnv({ LIVE_NO_PUSH: { class: "secret" } })).not.toThrow();
  });

  // `ocel dev` has no membrane and so no push; it delivers a live value the way
  // it delivers every other class, through the environment. A read falls
  // through to it whenever no push holds the key, which is what keeps dev and
  // production the same call site.
  it("falls through to the environment for a live key when nothing was pushed", () => {
    vi.stubEnv("OCEL_VAR_LIVE_IN_DEV", "sk_dev_123");
    const env = defineEnv({ LIVE_IN_DEV: { class: "secret" } });

    expect(env.LIVE_IN_DEV).toBe("sk_dev_123");
  });

  // The bare name is the one `ocel dev` actually uses: it merges the values it
  // resolved into the child's environment under the key the application
  // declared, with no namespacing, because the namespaced spelling belongs to
  // the encrypted-baked path a deploy builds and dev never builds one. A
  // fall-through that only consulted the namespaced name would leave every dev
  // run declaring a live key reading as unset.
  it("falls through to the bare name dev delivers a live value under", () => {
    vi.stubEnv("LIVE_IN_DEV_BARE", "sk_dev_bare");
    const env = defineEnv({ LIVE_IN_DEV_BARE: { class: "secret" } });

    expect(env.LIVE_IN_DEV_BARE).toBe("sk_dev_bare");
  });

  // The push still outranks the environment where both exist: a deploy never
  // sets these names for a live key, so an environment entry under one in
  // production is something else wearing the name.
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
// delivery resolves each value's *coordinate*, folder included, before the
// runtime sees anything.
//
// For every class but one that also means the value itself is resolved first.
// A live value is the exception: only its coordinate is pinned at deploy, and
// the value is fetched while the process runs. It is no exception to the
// guarantee, because what the membrane spends that coordinate on never crosses
// back — it pushes a flat map under bare key names, and the runtime asks for a
// live value by bare key exactly as it asks for any other. The folder is spent
// at deploy either way; what differs is only when the value is fetched.
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

  // A live value is fetched while the process runs rather than resolved before
  // it starts, so it is the one delivery this suite's charter has to be
  // re-argued for. The push is planted with folder-shaped entries beside the
  // bare one, exactly as plant does for the environment: a read that addressed
  // the pushed map by folder would return one of those instead.
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

// The other half of ocelhq-xd5j.34. The CLI half is pinned in Go — that
// `resolvedEnv` states OCEL_APP_FOLDER and that the child is spawned with it
// (cli/internal/cli/devenv_test.go). Neither side alone shows that the pairing
// works: the CLI cannot see whether the SDK accepts what it sent, and every
// scope test above stubs a binding no dev run ever produced. These replay the
// environment `ocel dev` actually builds and read out of it.
//
// Three properties of that environment are load-bearing here and are modelled
// rather than assumed. Every value arrives under its bare name, because dev has
// no membrane and the environment is its only delivery channel — so no class
// gets the OCEL_VAR_ namespacing a deploy would give it, and no live value
// arrives by push. The binding is written unconditionally, empty included, so
// an unbound project states the root rather than inheriting a stale one. And a
// scoped key's value is the same string at every folder its scope names,
// because dev's store broadcasts one flat root entry across them.
describe("a scoped read under `ocel dev`", () => {
  // devChildEnv is `resolvedEnv` (cli/internal/cli/dev.go) in the SDK's terms:
  // the flat map under bare names, with the binding written last and always.
  // Anything the deploy path would add is deliberately absent.
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

  // A live key in dev has no push behind it, so it resolves through the bare
  // environment fallback. That fallback and the scope check are the two things
  // standing between this read and a throw, and this is the only test that puts
  // both in the position a dev run puts them in.
  it("resolves a scoped live value through dev's environment delivery", () => {
    devChildEnv("/web", { DEV_SCOPED_LIVE: "sk_dev" });

    const env = defineEnv({
      DEV_SCOPED_LIVE: { class: "secret", folders: ["/web"] },
    });

    expect(env.DEV_SCOPED_LIVE).toBe("sk_dev");
  });

  // The broadcast, read from the SDK's side: one root line in the file becomes
  // the value at every folder the declaration names, so an app bound to either
  // reads it and gets the same string. That the two cannot diverge is the cost
  // the CLI's notice states.
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

  // The stated limit of .34 under dev, pinned as behaviour rather than left as
  // prose: dev spawns one child for the whole project, so two apps binding
  // different folders make `appbuilder.AppFolder` state the project root. The
  // value is delivered — the broadcast put it in the flat map under its bare
  // name, and it is right there in this environment — and the read is still
  // refused. That refusal is correct: handing it over would give one app the
  // other's scoped value. This test is what makes the limit visible if anyone
  // ever "fixes" it by loosening the scope check instead of by teaching dev
  // which app the child is.
  it("still refuses a scoped read when dev could only state the project root", () => {
    devChildEnv("", { DEV_TWO_APPS: "delivered but not this app's" });

    const env = defineEnv({
      DEV_TWO_APPS: { class: "plain", folders: ["/web"] },
    });

    expect(() => env.DEV_TWO_APPS).toThrow(EnvScopeError);
    expect(process.env.DEV_TWO_APPS).toBe("delivered but not this app's");
  });

  // Dev writes the binding unconditionally so a value left in the developer's
  // own shell cannot answer for the run. From the SDK's side that shows up as
  // the empty string dev wrote winning over the stale one, which is only
  // observable because the two disagree.
  it("honours the empty binding dev wrote over a stale one from the shell", () => {
    vi.stubEnv("OCEL_APP_FOLDER", "/web");
    devChildEnv("", { DEV_STALE_BINDING: "not this app's either" });

    const env = defineEnv({
      DEV_STALE_BINDING: { class: "plain", folders: ["/web"] },
    });

    expect(() => env.DEV_STALE_BINDING).toThrow(/project root/);
  });
});
