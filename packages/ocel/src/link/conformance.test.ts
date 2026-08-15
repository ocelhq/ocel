import { existsSync, readFileSync, readdirSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { link } from "./index.js";
import { type OwnedToken, shapes } from "./registry.js";

function repoRoot() {
  let dir = new URL("./", import.meta.url);
  for (;;) {
    if (existsSync(new URL("go.work", dir))) return dir;
    const parent = new URL("../", dir);
    if (parent.href === dir.href) {
      throw new Error("no go.work found above the link package");
    }
    dir = parent;
  }
}

const fixtures = new URL("proto/links/v1/fixtures/", repoRoot());

function fileOf(token: string) {
  return `${token.replaceAll(":", "-")}.json`;
}

function raw(token: string) {
  return readFileSync(new URL(fileOf(token), fixtures), "utf8");
}

const tokens = Object.keys(shapes) as OwnedToken[];

describe("the link-record conformance fixtures", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("carry exactly one record per ocel-owned token", () => {
    expect(readdirSync(fixtures).sort()).toEqual(tokens.map(fileOf).sort());
  });

  it.each(tokens)("delivers %s to the escape hatch verbatim", (token) => {
    const body = raw(token);
    vi.stubEnv("OCEL_LINK_conformance", body);

    expect(link.custom("conformance", token)).toEqual(
      JSON.parse(body).properties,
    );
  });

  it("types the postgres fixture through its own accessor", () => {
    const body = raw("ocel:postgres");
    vi.stubEnv("OCEL_LINK_main", body);

    expect(link.postgres("main")).toEqual(JSON.parse(body).properties);
  });

  it("types the bucket fixture through its own accessor", () => {
    const body = raw("ocel:bucket");
    vi.stubEnv("OCEL_LINK_uploads", body);

    expect(link.bucket("uploads")).toEqual(JSON.parse(body).properties);
  });
});
