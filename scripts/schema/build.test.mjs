import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, relative } from "node:path";
import { afterEach, beforeEach, describe, it } from "node:test";
import { toECMAScriptPattern, writeSchemas } from "./build.mjs";

const merged = {
  type: "object",
  properties: {
    provider: {
      oneOf: [
        { type: "string", enum: [] },
        { type: "object", additionalProperties: false, properties: {} },
      ],
    },
  },
};

const messages = [{ name: "cli.stream.v1.RunEvent", bundle: { type: "object" } }];

describe("writeSchemas", () => {
  let root;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ocel-schema-build-"));
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

  it("writes nothing under a released version's directory, whatever the release version", () => {
    writeSchemas(root, merged, messages);
    const written = readdirSync(root, { recursive: true, withFileTypes: true })
      .filter((entry) => entry.isFile())
      .map((entry) => relative(root, join(entry.parentPath, entry.name)));
    const versioned = written.filter((path) => /(^|\/)\d+\.\d+\.\d+(\/|$)/.test(path));
    assert.deepEqual(versioned, []);
  });

  it("publishes each CLI result schema at its unversioned URL", () => {
    writeSchemas(root, merged, messages);
    const published = JSON.parse(
      readFileSync(
        join(root, "www", "public", "schema", "cli", "cli.stream.v1.RunEvent.schema.json"),
        "utf8",
      ),
    );
    assert.equal(
      published.$id,
      "https://docs.example.test/schema/cli/cli.stream.v1.RunEvent.schema.json",
    );
  });
});

describe("toECMAScriptPattern", () => {
  it("drops the escape from a character JavaScript's unicode mode refuses to see escaped", () => {
    const uri =
      "^(?:(?:[a-zA-Z][a-zA-Z\\d+\\-.]*):)?(?://(?:[A-Za-z0-9\\-\\.]+(?::\\d+)?))?(/[^\\?#]*)?(?:\\?([^\\#]*))?(?:\\#(.*))?$";
    const pattern = toECMAScriptPattern(uri);
    const regexp = new RegExp(pattern, "u");
    assert.equal(
      pattern,
      "^(?:(?:[a-zA-Z][a-zA-Z\\d+\\-.]*):)?(?://(?:[A-Za-z0-9\\-\\.]+(?::\\d+)?))?(/[^\\?#]*)?(?:\\?([^#]*))?(?:#(.*))?$",
    );
    assert.ok(regexp.test("https://example.com/a?b#c"));
  });

  it("keeps an escaped backslash and every escape that means something", () => {
    assert.equal(toECMAScriptPattern("^\\\\#\\.\\/\\d$"), "^\\\\#\\.\\/\\d$");
  });
});
