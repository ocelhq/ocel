import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { buildProcess, runAdapterBuild } from "../src/script.mjs";

describe("runAdapterBuild", () => {
  const realSpawn = buildProcess.spawn;
  afterEach(() => {
    buildProcess.spawn = realSpawn;
  });

  const adapter = {
    framework: "sveltekit",
    name: "@ocel/sveltekit",
    setup: "Add it as the adapter",
    upgrade: "Install the @ocel/sveltekit release that matches this CLI",
  };

  function app() {
    const cwd = mkdtempSync(path.join(tmpdir(), "ocel-adapter-build-"));
    writeFileSync(path.join(cwd, "package.json"), JSON.stringify({ scripts: { build: "x" } }));
    const outputDir = path.join(cwd, "out");
    mkdirSync(outputDir);
    return { name: "web", cwd, outputDir, folder: "/web" };
  }

  it("runs the build with where to write, which app it is and its folder, and returns the hosting", async () => {
    const built = app();
    let env: Record<string, string> = {};
    buildProcess.spawn = async (_command, _args, _cwd, e) => {
      env = e;
      writeFileSync(
        path.join(built.outputDir, "hosting.json"),
        '{"version":1,"framework":"sveltekit"}',
      );
    };

    const hosting = await runAdapterBuild(built, adapter, { OCEL_BUILD_ID: "b1" });

    expect(env).toMatchObject({
      NODE_ENV: "production",
      OCEL_APP_NAME: "web",
      OCEL_OUTPUT_DIR: built.outputDir,
      OCEL_APP_FOLDER: "/web",
      OCEL_BUILD_ID: "b1",
    });
    expect(hosting.framework).toBe("sveltekit");
  });

  it("says how to set the adapter up when the build wrote no hosting", async () => {
    buildProcess.spawn = async () => {};

    await expect(runAdapterBuild(app(), adapter, {})).rejects.toThrow(
      /nothing wrote hosting\.json.*@ocel\/sveltekit\. Add it as the adapter/,
    );
  });

  it("refuses hosting another adapter wrote", async () => {
    const built = app();
    buildProcess.spawn = async () =>
      writeFileSync(path.join(built.outputDir, "hosting.json"), '{"framework":"node"}');

    await expect(runAdapterBuild(built, adapter, {})).rejects.toThrow(/names framework "node"/);
  });

  it("refuses hosting of a version this CLI does not read, saying how to match it", async () => {
    const built = app();
    buildProcess.spawn = async () =>
      writeFileSync(
        path.join(built.outputDir, "hosting.json"),
        '{"version":2,"framework":"sveltekit"}',
      );

    await expect(runAdapterBuild(built, adapter, {})).rejects.toThrow(
      /version 2.*Install the @ocel\/sveltekit release/,
    );
  });
});
