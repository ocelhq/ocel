import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { describe, it } from "node:test";
import {
  declaredLicense,
  installedDirectory,
  npmComponent,
  OVERRIDES,
  overrideFor,
  PERMISSIVE,
  parseBuildInfo,
  permitted,
  render,
  storeDirectory,
  violations,
} from "./notices.mjs";

describe("permitted", () => {
  it("takes a permissive id", () => {
    assert.ok(permitted("MIT"));
    assert.ok(permitted("Apache-2.0"));
  });

  it("takes a choice when any branch is permissive", () => {
    assert.ok(permitted("(MIT OR GPL-3.0-only)"));
  });

  it("refuses a conjunction with a copyleft term", () => {
    assert.equal(permitted("MIT AND LGPL-3.0-or-later"), false);
  });

  it("refuses copyleft outright", () => {
    for (const id of ["GPL-2.0-only", "LGPL-3.0-or-later", "AGPL-3.0-only"]) {
      assert.equal(permitted(id), false, id);
    }
  });

  it("judges the license an exception is attached to", () => {
    assert.ok(permitted("Apache-2.0 WITH LLVM-exception"));
  });

  it("refuses what it cannot parse", () => {
    assert.equal(permitted("SEE LICENSE IN LICENSE.md"), false);
    assert.equal(permitted("(MIT"), false);
  });

  it("allows nothing copyleft", () => {
    for (const id of PERMISSIVE) assert.doesNotMatch(id, /GPL/);
  });
});

describe("declaredLicense", () => {
  it("reads the spdx string", () => {
    assert.equal(declaredLicense({ license: "ISC" }), "ISC");
  });

  it("reads the legacy object and array forms", () => {
    assert.equal(declaredLicense({ license: { type: "MIT" } }), "MIT");
    assert.equal(
      declaredLicense({ licenses: [{ type: "MIT" }, { type: "Apache-2.0" }] }),
      "(MIT OR Apache-2.0)",
    );
  });

  it("reports nothing when the manifest declares nothing", () => {
    assert.equal(declaredLicense({}), undefined);
  });
});

describe("storeDirectory", () => {
  it("places a scoped package in the pnpm store, whatever the path is relative to", () => {
    assert.equal(
      storeDirectory(
        "../../node_modules/.pnpm/@smithy+core@3.31.1/node_modules/@smithy/core/dist-cjs/index.js",
      ),
      "node_modules/.pnpm/@smithy+core@3.31.1/node_modules/@smithy/core",
    );
  });

  it("places an unscoped package", () => {
    assert.equal(
      storeDirectory("/r/node_modules/.pnpm/bowser@2.14.1/node_modules/bowser/es5.js"),
      "node_modules/.pnpm/bowser@2.14.1/node_modules/bowser",
    );
  });

  it("leaves workspace sources alone", () => {
    assert.equal(storeDirectory("frameworks/next/cache/src/index.mts"), undefined);
  });
});

describe("installedDirectory", () => {
  it("finds a package installed in a zipped node_modules", () => {
    assert.equal(
      installedDirectory("node_modules/@img/colour/package.json"),
      "node_modules/@img/colour",
    );
    assert.equal(installedDirectory("node_modules/sharp/package.json"), "node_modules/sharp");
  });

  it("ignores a manifest nested inside a package", () => {
    assert.equal(installedDirectory("node_modules/sharp/lib/package.json"), undefined);
    assert.equal(installedDirectory("node_modules/.bin/package.json"), undefined);
  });
});

describe("parseBuildInfo", () => {
  it("reads the main package, target and tags", () => {
    const info = parseBuildInfo(
      [
        "/tmp/bootstrap: go1.27.0",
        "\tpath\tgithub.com/ocelhq/ocel/platform/aws/runtime/cmd/runtime",
        "\tmod\tgithub.com/ocelhq/ocel/platform/aws/runtime\t(devel)\t",
        "\tbuild\t-tags=lambda.norpc",
        "\tbuild\tGOARCH=arm64",
        "\tbuild\tGOOS=linux",
        "",
      ].join("\n"),
    );
    assert.deepEqual(info, {
      path: "github.com/ocelhq/ocel/platform/aws/runtime/cmd/runtime",
      goos: "linux",
      goarch: "arm64",
      tags: "lambda.norpc",
      goVersion: "go1.27.0",
    });
  });
});

describe("violations", () => {
  it("names each component the gate refuses", () => {
    const found = violations([
      { ecosystem: "npm", name: "ok", version: "1.0.0", license: "MIT" },
      { ecosystem: "npm", name: "vips", version: "1.0.0", license: "LGPL-3.0-or-later" },
      { ecosystem: "npm", name: "bare", version: "1.0.0" },
    ]);
    assert.equal(found.length, 2);
    assert.match(found[0], /vips@1\.0\.0: LGPL-3\.0-or-later/);
    assert.match(found[1], /bare@1\.0\.0: declares no license/);
  });
});

describe("OVERRIDES", () => {
  it("says why each entry is needed, and carries the texts a non-permissive license requires", () => {
    for (const [name, entry] of Object.entries(OVERRIDES)) {
      assert.ok(entry.reason.length > 0, name);
      if (!PERMISSIVE.has(entry.license)) {
        assert.ok(entry.texts.length > 0, name);
        assert.equal(typeof entry.source, "function", name);
      }
    }
  });

  it("matches a trailing wildcard by prefix, and nothing else", () => {
    assert.equal(overrideFor("@img/sharp-libvips-linux-arm64"), OVERRIDES["@img/sharp-libvips-*"]);
    assert.equal(overrideFor("@img/sharp-linux-arm64"), undefined);
    assert.equal(overrideFor("github.com/segmentio/asm"), OVERRIDES["github.com/segmentio/asm"]);
  });
});

describe("npmComponent", () => {
  function fixture(manifest) {
    const dir = join(mkdtempSync(join(tmpdir(), "ocel-notices-test-")), "pkg");
    mkdirSync(dir);
    writeFileSync(join(dir, "package.json"), JSON.stringify(manifest));
    writeFileSync(join(dir, "versions.json"), JSON.stringify({ vips: "8.18.3" }));
    writeFileSync(join(dir, "README.md"), "# licensing table\n");
    return dir;
  }

  it("accepts libvips under its override with the LGPL and GPL texts and a source pointer", () => {
    const dir = fixture({
      name: "@img/sharp-libvips-linux-arm64",
      version: "1.3.2",
      license: "LGPL-3.0-or-later",
    });
    try {
      const component = npmComponent(dir);
      assert.equal(component.accepted, true);
      assert.deepEqual(violations([component]), []);
      const texts = component.texts.map((entry) => entry.text).join("\n");
      assert.match(texts, /GNU LESSER GENERAL PUBLIC LICENSE\s+Version 3/);
      assert.match(texts, /GNU GENERAL PUBLIC LICENSE\s+Version 3/);
      const source = component.notices.find((entry) => entry.name === "corresponding source");
      assert.match(source.text, /libvips 8\.18\.3/);
      assert.match(source.text, /github\.com\/lovell\/sharp-libvips\/tree\/v1\.3\.2/);
      assert.ok(component.notices.some((entry) => entry.name === "README.md"));
      assert.ok(component.notices.some((entry) => entry.name === "versions.json"));
    } finally {
      rmSync(dirname(dir), { recursive: true, force: true });
    }
  });

  it("still refuses a package under the override that changes its license", () => {
    const dir = fixture({
      name: "@img/sharp-libvips-linux-arm64",
      version: "9.0.0",
      license: "GPL-3.0-only",
    });
    try {
      const component = npmComponent(dir);
      assert.equal(component.accepted, undefined);
      assert.equal(violations([component]).length, 1);
    } finally {
      rmSync(dirname(dir), { recursive: true, force: true });
    }
  });

  it("leaves LGPL off the allowlist for everything else", () => {
    const dir = fixture({ name: "vips-lookalike", version: "1.0.0", license: "LGPL-3.0-or-later" });
    try {
      assert.equal(violations([npmComponent(dir)]).length, 1);
    } finally {
      rmSync(dirname(dir), { recursive: true, force: true });
    }
  });
});

describe("render", () => {
  const mit = (name) => ({
    ecosystem: "npm",
    name,
    version: "1.0.0",
    license: "MIT",
    texts: [{ name: "LICENSE", text: "MIT text\n" }],
    notices: [],
  });

  it("prints a shared license text once, under every component that ships it", () => {
    const text = render({
      binary: "ocel",
      target: "linux/amd64",
      components: [mit("b"), mit("a")],
      toolchains: new Set(["go1.27.0"]),
      goLicense: "Go text",
      apacheLicense: "Apache text",
    });
    assert.equal(text.match(/MIT text/g).length, 1);
    assert.match(text, /npm a 1\.0\.0\nnpm b 1\.0\.0\n-+\n\nMIT text/);
    assert.match(text, /go standard library go1\.27\.0\n-+\n\nGo text/);
    assert.doesNotMatch(text, /Apache text/);
  });

  it("carries NOTICE files and the Apache license an Apache component points to", () => {
    const text = render({
      binary: "ocel",
      target: "linux/amd64",
      components: [
        {
          ecosystem: "go",
          name: "example.com/a",
          version: "v1.0.0",
          license: "Apache-2.0",
          texts: [],
          notices: [{ name: "NOTICE", text: "Attribution a\n" }],
        },
      ],
      toolchains: new Set(),
      goLicense: "Go text",
      apacheLicense: "Apache text",
    });
    assert.match(text, /go example\.com\/a v1\.0\.0: NOTICE\n-+\n\nAttribution a/);
    assert.match(text, /Apache text\n$/);
    assert.doesNotMatch(text, /Go text/);
  });

  it("renders the same input to the same bytes", () => {
    const input = () => ({
      binary: "ocel",
      target: "darwin/arm64",
      components: [mit("z"), mit("a")],
      toolchains: new Set(["go1.27.0"]),
      goLicense: "Go text",
      apacheLicense: "Apache text",
    });
    assert.equal(render(input()), render({ ...input(), components: [mit("a"), mit("z")] }));
  });
});
