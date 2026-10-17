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
    mkdirSync(join(root, "cli", "internal", "docsurl"), { recursive: true });
  });

  afterEach(() => {
    rmSync(root, { recursive: true, force: true });
  });

  const writeDocsURL = (source) =>
    writeFileSync(join(root, "cli", "internal", "docsurl", "docsurl.go"), source);

  it("reads the origin the CLI's docsurl.Origin declares", () => {
    writeDocsURL(
      'package docsurl\n\nconst Origin = "https://docs.example.test"\n\nconst Schema = Origin + "/schema/ocel.schema.json"\n',
    );
    assert.equal(readDocsOrigin(root), "https://docs.example.test");
  });

  it("refuses a docsurl.go that declares no Origin constant", () => {
    writeDocsURL('package docsurl\n\nconst Schema = "https://docs.example.test/schema.json"\n');
    assert.throws(() => readDocsOrigin(root), /docsurl\.go declares no Origin constant/);
  });
});
