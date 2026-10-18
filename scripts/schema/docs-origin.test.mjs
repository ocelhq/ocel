import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, it } from "node:test";
import { readDocsOrigin } from "./docs-origin.mjs";

describe("readDocsOrigin", () => {
  let root;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ocel-docs-origin-"));
    mkdirSync(join(root, "cli", "internal", "commands", "projectinit"), { recursive: true });
  });

  afterEach(() => {
    rmSync(root, { recursive: true, force: true });
  });

  const writeInit = (source) =>
    writeFileSync(join(root, "cli", "internal", "commands", "projectinit", "init.go"), source);

  it("reads the origin of the schema URL ocel init writes into a project", () => {
    writeInit(
      'package projectinit\n\nconst configSchema = "https://docs.example.test/schema/ocel.schema.json"\n',
    );
    assert.equal(readDocsOrigin(root), "https://docs.example.test");
  });

  it("refuses an init.go whose configSchema is not the published config schema", () => {
    writeInit(
      'package projectinit\n\nconst configSchema = "https://docs.example.test/schema.json"\n',
    );
    assert.throws(() => readDocsOrigin(root), /declares no configSchema constant/);
  });
});
