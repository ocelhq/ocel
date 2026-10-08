import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, expect, test } from "vitest";
import { writeLibvipsNotice } from "../scripts/stage-sharp.mjs";

const pkgDir = dirname(dirname(fileURLToPath(import.meta.url)));

let out;

beforeEach(() => {
  out = mkdtempSync(join(tmpdir(), "ocel-libvips-notice-"));
});

afterEach(() => {
  rmSync(out, { recursive: true, force: true });
});

function stageLibvips(cpu, version, vips) {
  const libvips = join(out, "node_modules", "@img", `sharp-libvips-linux-${cpu}`);
  mkdirSync(libvips, { recursive: true });
  writeFileSync(
    join(libvips, "package.json"),
    JSON.stringify({ name: `@img/sharp-libvips-linux-${cpu}`, version }),
  );
  writeFileSync(join(libvips, "versions.json"), JSON.stringify({ vips }));
}

test("the staged sharp names the libvips it ships, its license and where its source is", () => {
  stageLibvips("arm64", "1.3.2", "8.18.3");

  writeLibvipsNotice(out, "arm64");

  const notice = readFileSync(join(out, "THIRD_PARTY_NOTICES"), "utf8");
  expect(notice).toContain("libvips 8.18.3");
  expect(notice).toContain("node_modules/@img/sharp-libvips-linux-arm64");
  expect(notice).toContain("LGPL-3.0-or-later");
  expect(notice).toContain("https://github.com/lovell/sharp-libvips/releases/tag/v1.3.2");
});

test("the staged sharp carries the LGPL-3.0 and GPL-3.0 texts verbatim", () => {
  stageLibvips("x64", "1.3.2", "8.18.3");

  writeLibvipsNotice(out, "x64");

  for (const [text, heading] of [
    ["LGPL-3.0.txt", "GNU LESSER GENERAL PUBLIC LICENSE"],
    ["GPL-3.0.txt", "GNU GENERAL PUBLIC LICENSE"],
  ]) {
    const shipped = readFileSync(join(out, text), "utf8");
    expect(shipped).toBe(readFileSync(join(pkgDir, "licenses", text), "utf8"));
    expect(shipped).toContain(heading);
  }
});
