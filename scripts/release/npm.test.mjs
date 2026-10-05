import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";
import { distTag, LICENSING, ORDER, pack, published } from "./npm.mjs";

const packages = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "packages");

describe("distTag", () => {
  it("puts a stable version on latest", () => {
    assert.equal(distTag("0.1.0"), "latest");
  });

  it("puts a release candidate on next", () => {
    assert.equal(distTag("0.1.0-rc.3"), "next");
  });

  it("puts a nightly on nightly", () => {
    assert.equal(distTag("0.1.1-0.nightly.20260923.gabc1234"), "nightly");
  });

  it("refuses a version of no channel", () => {
    assert.throws(() => distTag("0.1.0-beta.1"));
  });
});

describe("ORDER", () => {
  it("names every package, each exactly once", () => {
    const present = readdirSync(packages, { withFileTypes: true })
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort();
    assert.deepEqual([...ORDER].sort(), present);
  });

  it("publishes the platform binaries before the cli that depends on them", () => {
    const cli = ORDER.indexOf("cli");
    for (const dir of ORDER.filter((name) => name.startsWith("cli-"))) {
      assert.ok(ORDER.indexOf(dir) < cli, `${dir} comes after cli`);
    }
  });

  it("pins the cli to the platform binaries of its own version", () => {
    const manifest = JSON.parse(readFileSync(join(packages, "cli", "package.json"), "utf8"));
    for (const [name, range] of Object.entries(manifest.optionalDependencies)) {
      assert.equal(range, "workspace:*", name);
    }
  });

  it("installs every workspace peer, so pnpm pack can resolve its range", () => {
    for (const dir of ORDER) {
      const manifest = JSON.parse(readFileSync(join(packages, dir, "package.json"), "utf8"));
      for (const [name, range] of Object.entries(manifest.peerDependencies ?? {})) {
        if (range.startsWith("workspace:")) {
          assert.equal(manifest.devDependencies?.[name], range, `${dir} peer ${name}`);
        }
      }
    }
  });

  it("packs the licensing files the publish copies in from the root", () => {
    for (const dir of ORDER) {
      const manifest = JSON.parse(readFileSync(join(packages, dir, "package.json"), "utf8"));
      for (const file of LICENSING)
        assert.ok(manifest.files.includes(file), `${dir} lacks ${file}`);
      assert.equal(manifest.license, "Apache-2.0", dir);
    }
  });

  it("gives every package the repository npm provenance checks", () => {
    for (const dir of ORDER) {
      const manifest = JSON.parse(readFileSync(join(packages, dir, "package.json"), "utf8"));
      assert.equal(manifest.repository?.url, "git+https://github.com/ocelhq/ocel.git", dir);
      assert.equal(manifest.repository?.directory, `packages/${dir}`, dir);
    }
  });
});

describe("published", () => {
  const answering = (result) => (_command, _args, options) => {
    assert.ok(!existsSync(join(options.cwd, "package.json")), "npm view runs beside a manifest");
    return { stdout: "", stderr: "", ...result };
  };

  it("asks npm from a directory no manifest's devEngines governs", () => {
    published("@ocel/cli", "0.1.0", answering({ status: 0, stdout: '"0.1.0"' }));
  });

  it("reads a version npm returns as published", () => {
    assert.equal(
      published("@ocel/cli", "0.1.0", answering({ status: 0, stdout: '"0.1.0"' })),
      true,
    );
  });

  it("reads an E404 as not published", () => {
    const stdout = JSON.stringify({ error: { code: "E404", summary: "No match found" } });
    assert.equal(published("@ocel/cli", "0.1.0", answering({ status: 1, stdout })), false);
  });

  it("refuses to read any other failure as not published", () => {
    const stdout = JSON.stringify({ error: { code: "EBADDEVENGINES" } });
    assert.throws(
      () =>
        published("@ocel/cli", "0.1.0", answering({ status: 1, stdout, stderr: "EBADDEVENGINES" })),
      /EBADDEVENGINES/,
    );
  });
});

describe("pack", () => {
  it("keeps every platform binary executable in the tarball", () => {
    const root = mkdtempSync(join(tmpdir(), "ocel-pack-"));
    try {
      for (const dir of ORDER.filter((name) => name.startsWith("cli-"))) {
        const binary = dir === "cli-win32-x64" ? "ocel.exe" : "ocel";
        const cwd = join(root, dir);
        mkdirSync(join(cwd, "bin"), { recursive: true });
        copyFileSync(join(packages, dir, "package.json"), join(cwd, "package.json"));
        for (const file of LICENSING) writeFileSync(join(cwd, file), "");
        writeFileSync(join(cwd, "bin", binary), "#!/bin/sh\n");
        chmodSync(join(cwd, "bin", binary), 0o755);
        const destination = mkdtempSync(join(root, "pack-"));
        const tarball = pack(cwd, destination);
        const listing = execFileSync("tar", ["-tvzf", tarball], { encoding: "utf8" });
        const entry = listing.split("\n").find((line) => line.endsWith(`package/bin/${binary}`));
        assert.match(entry ?? "", /^-rwxr-xr-x/, dir);
      }
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
