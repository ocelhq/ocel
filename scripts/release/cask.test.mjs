import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { cask } from "./cask.mjs";

const checksums = ["darwin_arm64", "darwin_amd64", "linux_arm64", "linux_amd64", "windows_amd64"]
  .map((target, index) => {
    const extension = target.startsWith("windows") ? "zip" : "tar.gz";
    return `${String(index).repeat(64)}  ocel_0.1.0_${target}.${extension}`;
  })
  .join("\n");

describe("cask", () => {
  it("points each platform at its release archive and the sum checksums.txt gives it", () => {
    const rendered = cask("0.1.0", checksums);
    assert.match(rendered, /version "0\.1\.0"/);
    assert.ok(
      rendered.includes(
        `sha256 "${"3".repeat(64)}"\n      url "https://github.com/ocelhq/ocel/releases/download/v0.1.0/ocel_0.1.0_linux_amd64.tar.gz"`,
      ),
    );
    assert.ok(rendered.includes(`sha256 "${"0".repeat(64)}"`));
    assert.ok(!rendered.includes("windows"));
  });

  it("refuses a checksums.txt missing a platform the cask installs", () => {
    const partial = checksums
      .split("\n")
      .filter((line) => !line.includes("linux_arm64"))
      .join("\n");
    assert.throws(() => cask("0.1.0", partial), /linux_arm64/);
  });

  it("refuses a prerelease, which the tap never publishes", () => {
    assert.throws(() => cask("0.1.0-rc.1", checksums), /stable/);
  });
});
