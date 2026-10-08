import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { buildProcess } from "@framework/node-build/script";
import { afterEach, describe, expect, it } from "vitest";
import { buildSvelteKit, type SvelteKitBuild } from "../src/build.mjs";

const realSpawn = buildProcess.spawn;
afterEach(() => {
  buildProcess.spawn = realSpawn;
});

function app(): SvelteKitBuild {
  const cwd = mkdtempSync(join(tmpdir(), "ocel-sveltekit-build-"));
  writeFileSync(join(cwd, "package.json"), JSON.stringify({ scripts: { build: "vite build" } }));
  const outputDir = join(cwd, "out");
  mkdirSync(outputDir);
  return { name: "web", cwd, outputDir, buildId: "b1" };
}

describe("buildSvelteKit", () => {
  it("runs the app's build script with where to write and what the build is", async () => {
    const built = app();
    let env: Record<string, string> = {};
    buildProcess.spawn = async (_command, _args, _cwd, e) => {
      env = e;
      writeFileSync(join(built.outputDir, "hosting.json"), '{"version":1,"framework":"sveltekit"}');
    };

    await buildSvelteKit(built);

    expect(env).toMatchObject({
      NODE_ENV: "production",
      OCEL_APP_NAME: "web",
      OCEL_OUTPUT_DIR: built.outputDir,
      OCEL_BUILD_ID: "b1",
    });
  });

  it("names the adapter to add when the build wrote no hosting", async () => {
    buildProcess.spawn = async () => {};

    await expect(buildSvelteKit(app())).rejects.toThrow(/sveltekit\(\{ adapter: ocel\(\) \}\)/);
  });

  it("refuses hosting another adapter wrote", async () => {
    const built = app();
    buildProcess.spawn = async () =>
      writeFileSync(join(built.outputDir, "hosting.json"), '{"framework":"node"}');

    await expect(buildSvelteKit(built)).rejects.toThrow(/names framework "node"/);
  });

  it("refuses hosting of a version this CLI does not read, naming the adapter release", async () => {
    const built = app();
    buildProcess.spawn = async () =>
      writeFileSync(join(built.outputDir, "hosting.json"), '{"version":2,"framework":"sveltekit"}');

    await expect(buildSvelteKit(built)).rejects.toThrow(/version 2.*@ocel\/sveltekit/);
  });
});
