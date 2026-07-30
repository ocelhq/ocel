import { beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

const declareEnvMock = vi.hoisted(() =>
  vi.fn(() => Promise.resolve({ cells: [] as unknown[] })),
);
const reportEnvProblemsMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

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

const { defineEnv, EnvDefinitionError, EnvValueError } = await import(
  "./index.js"
);

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

beforeEach(() => {
  declareEnvMock.mockClear();
  reportEnvProblemsMock.mockClear();
  declareEnvMock.mockResolvedValue({ cells: [] });
  globalThis.__ocelRegister = [];
});

describe("definition errors", () => {
  it("rejects a key already declared by another defineEnv call", () => {
    defineEnv({ DUPE_KEY: { class: "plain" } });
    expect(() => defineEnv({ DUPE_KEY: { class: "secret" } })).toThrow(
      EnvDefinitionError,
    );
    expect(() => defineEnv({ DUPE_KEY: { class: "secret" } })).toThrow(
      /DUPE_KEY/,
    );
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

  it("rejects client access on an encrypted class", () => {
    expect(() =>
      defineEnv({ CLIENT_SENSITIVE: { class: "sensitive", client: true } }),
    ).toThrow(/client/i);
    expect(() =>
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
    expect(declareEnvMock).toHaveBeenCalledWith({
      definitions: [
        {
          key: "PAYLOAD_PLAIN",
          class: 1,
          clientAccessible: true,
          required: true,
        },
        {
          key: "PAYLOAD_SECRET",
          class: 3,
          clientAccessible: false,
          required: true,
        },
        {
          key: "PAYLOAD_DEFAULTED",
          class: 2,
          clientAccessible: false,
          required: false,
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

describe("reading a variable", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  it("reads as a plain synchronous property", () => {
    vi.stubEnv("READ_PLAIN", "hello");
    const env = defineEnv({ READ_PLAIN: { class: "plain" } });
    expect(env.READ_PLAIN).toBe("hello");
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
