import assert from "node:assert/strict";
import { afterEach, describe, it } from "vitest";
import { EDGE_ENV } from "./aws.floci";
import { expectationsFor } from "./index";

const before = process.env[EDGE_ENV];

afterEach(() => {
  if (before === undefined) {
    delete process.env[EDGE_ENV];
  } else {
    process.env[EDGE_ENV] = before;
  }
});

describe("expectationsFor", () => {
  it("does not read the edge for an environment that has no edges", () => {
    process.env[EDGE_ENV] = "not-an-edge";
    assert.doesNotThrow(() => expectationsFor("dev"));
    assert.deepEqual(expectationsFor("aws"), {});
  });

  it("names the variable when the aws floci file is asked for an edge it does not list", () => {
    process.env[EDGE_ENV] = "not-an-edge";
    assert.throws(() => expectationsFor("aws.floci"), new RegExp(EDGE_ENV));
  });

  it("lists every planned title for an edge it knows", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const listed = expectationsFor("aws.floci");
    const cell = listed["express/web"];
    assert.ok(cell);
    assert.equal(cell.up, "https://github.com/ocelhq/ocel/issues/884");
    assert.equal(
      cell["GET /health answers with the app name"],
      "https://github.com/ocelhq/ocel/issues/854",
    );
    assert.equal(
      cell["GET /api/probes/stream streams its chunks in order to the sentinel"],
      "https://github.com/ocelhq/ocel/issues/851",
    );
    assert.equal(
      cell["redeploy · GET /health answers with the app name"],
      "https://github.com/ocelhq/ocel/issues/854",
    );
  });
});
