import { existsSync, readdirSync, readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BindingType } from "./gen/proto/common/bindings/v1/bindings_pb.js";
import { bindingKey } from "./utils/get-config.js";

vi.mock("./utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { postgres } = await import("./postgres/index.js");
const { bucket } = await import("./blob/bucket.js");

function repoRoot() {
  let dir = new URL("./", import.meta.url);
  for (;;) {
    if (existsSync(new URL("go.work", dir))) return dir;
    const parent = new URL("../", dir);
    if (parent.href === dir.href) {
      throw new Error("no go.work found above the ocel package");
    }
    dir = parent;
  }
}

const fixtures = new URL("proto/common/bindings/v1/fixtures/", repoRoot());

const types = Object.values(BindingType).filter(
  (value): value is BindingType =>
    typeof value === "number" && value !== BindingType.UNSPECIFIED && value !== BindingType.CUSTOM,
);

function fileOf(type: BindingType) {
  return `${BindingType[type].toLowerCase()}.json`;
}

function raw(type: BindingType) {
  return readFileSync(new URL(fileOf(type), fixtures), "utf8");
}

describe("the binding conformance fixtures", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("carry exactly one record per binding type an app resolves", () => {
    expect(readdirSync(fixtures).sort()).toEqual(types.map(fileOf).sort());
  });

  it("reach postgres() through its live key", () => {
    const body = raw(BindingType.POSTGRES);
    vi.stubEnv(bindingKey("main", BindingType.POSTGRES), body);
    const want = JSON.parse(body).postgres;

    const pool = postgres("main");

    expect(pool.options).toMatchObject({
      host: want.host,
      port: want.port,
      database: want.database,
      user: want.username,
      password: want.password,
    });
    expect(pool.connectionString).toBe(
      `postgres://${want.username}:${want.password}@${want.host}:${want.port}/${want.database}`,
    );
  });

  it("reach bucket() through its live key", () => {
    const body = raw(BindingType.BUCKET);
    vi.stubEnv(bindingKey("uploads", BindingType.BUCKET), body);

    expect(bucket("uploads", { uploaders: {} }).__config()).toMatchObject(JSON.parse(body).bucket);
  });
});
