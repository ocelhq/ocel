import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, it } from "node:test";
import { readPublishedSchemas } from "./published-schemas.mjs";

describe("readPublishedSchemas", () => {
  let root;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ocel-published-schemas-"));
    mkdirSync(join(root, "cli", "internal", "commands", "projectinit"), { recursive: true });
    writeFileSync(
      join(root, "cli", "internal", "commands", "projectinit", "init.go"),
      'package projectinit\n\nconst configSchema = "https://docs.example.test/schema/ocel.schema.json"\n',
    );
    writeFileSync(join(root, "VERSION"), "9.9.9\n");
  });

  afterEach(() => {
    rmSync(root, { recursive: true, force: true });
  });

  it("publishes the config schema to one path and URL, whatever the release version", () => {
    const { config } = readPublishedSchemas(root);
    assert.equal(config.path, join(root, "www", "public", "schema", "ocel.schema.json"));
    assert.equal(config.url, "https://docs.example.test/schema/ocel.schema.json");
  });

  it("publishes the CLI result schemas to one directory and URL, whatever the release version", () => {
    const { messages } = readPublishedSchemas(root);
    assert.equal(messages.dir, join(root, "www", "public", "schema", "cli"));
    assert.equal(messages.url, "https://docs.example.test/schema/cli");
  });
});
