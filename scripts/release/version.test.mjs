import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  parse,
  pep440,
  withPathPins,
  withSchemaURLs,
  withSection,
  withVersionLine,
} from "./version.mjs";

describe("parse", () => {
  it("reads a stable version", () => {
    assert.deepEqual(parse("0.0.1"), { channel: "stable", base: "0.0.1" });
  });

  it("reads a release candidate", () => {
    assert.deepEqual(parse("0.1.0-rc.2"), { channel: "rc", base: "0.1.0", rc: 2 });
  });

  it("reads a nightly", () => {
    assert.deepEqual(parse("0.0.2-0.nightly.20260923.gabc1234"), {
      channel: "nightly",
      base: "0.0.2",
      date: "20260923",
      sha: "abc1234",
    });
  });

  for (const refused of [
    "",
    "v0.0.1",
    "0.0",
    "01.0.0",
    "0.0.1-alpha.0",
    "0.0.1-rc.0",
    "0.0.1-rc",
    "0.0.2-nightly.20260923.gabc1234",
    "0.0.2-0.nightly.2026092.gabc1234",
    "0.0.2-0.nightly.20260923.abc1234",
    "0.0.2-0.nightly.20260923.gABC1234",
  ]) {
    it(`refuses ${JSON.stringify(refused)}`, () => {
      assert.throws(() => parse(refused));
    });
  }
});

describe("pep440", () => {
  it("keeps a stable version", () => {
    assert.equal(pep440("1.2.3"), "1.2.3");
  });

  it("writes a release candidate as rcN", () => {
    assert.equal(pep440("0.1.0-rc.3"), "0.1.0rc3");
  });

  it("writes a nightly as a dev release of its date", () => {
    assert.equal(pep440("0.0.2-0.nightly.20260923.gabc1234"), "0.0.2.dev20260923");
  });
});

describe("withSection", () => {
  const manifest = [
    "[project]",
    'name = "ocel"',
    'version = "0.0.1a0"',
    "",
    "[tool.x]",
    'version = "9"',
    "",
  ].join("\n");

  it("edits only the named section", () => {
    const stamped = withSection(manifest, "[project]", (section) =>
      withVersionLine(section, "0.0.0"),
    );
    assert.equal(stamped, manifest.replace('"0.0.1a0"', '"0.0.0"'));
  });

  it("edits a section that ends the file", () => {
    const stamped = withSection(manifest, "[tool.x]", (section) => withVersionLine(section, "1"));
    assert.equal(stamped, manifest.replace('"9"', '"1"'));
  });

  it("refuses a manifest without the section", () => {
    assert.throws(() => withSection(manifest, "[workspace.package]", (s) => s));
  });

  it("refuses a section without a version", () => {
    assert.throws(() => withSection("[a]\nname = 1\n", "[a]", (s) => withVersionLine(s, "1")));
  });
});

describe("withPathPins", () => {
  it("pins a sibling path dependency and leaves registry dependencies alone", () => {
    const manifest = [
      'ocel-macros = { path = "../ocel-macros", version = "0.0.1-alpha.0" }',
      'serde = { version = "1", features = ["derive"] }',
      'other = { version = "0.0.1-alpha.0", path = "../other" }',
    ].join("\n");
    assert.equal(
      withPathPins(manifest, "0.0.2"),
      [
        'ocel-macros = { path = "../ocel-macros", version = "0.0.2" }',
        'serde = { version = "1", features = ["derive"] }',
        'other = { version = "0.0.2", path = "../other" }',
      ].join("\n"),
    );
  });
});

describe("withSchemaURLs", () => {
  it("points every schema reference at the version", () => {
    const host = "https://ocel.dev/schema/";
    const text = `{"$schema": "${host}0.0.1-alpha.0/ocel.schema.json"}\n${host}0.0.3/ocel.schema.json`;
    assert.equal(
      withSchemaURLs(text, "0.0.4"),
      `{"$schema": "${host}0.0.4/ocel.schema.json"}\n${host}0.0.4/ocel.schema.json`,
    );
  });

  it("leaves a url the code builds from a version alone", () => {
    const text = `const url = \`https://ocel.dev/schema/\${version}/ocel.schema.json\`;`;
    assert.equal(withSchemaURLs(text, "0.0.4"), text);
  });
});
