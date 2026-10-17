import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { Refusal } from "../src/refusal.mjs";
import { type BuildLog, buildProcess, runAdapterBuild } from "../src/script.mjs";

const quiet: BuildLog = { print: () => {} };

describe("buildProcess.spawn", () => {
  it("refuses a build command that exits non-zero, naming the command and its code", async () => {
    const failed = buildProcess.spawn(
      "node",
      ["-e", "process.exit(3)"],
      tmpdir(),
      { PATH: process.env.PATH ?? "" },
      quiet,
    );

    await expect(failed).rejects.toThrow(/node -e process\.exit\(3\) exited with code 3/);
    await expect(failed).rejects.toBeInstanceOf(Refusal);
  });

  it("prints each line the build writes to stdout or stderr, a last unterminated one too", async () => {
    const printed: string[] = [];

    await buildProcess.spawn(
      "node",
      ["-e", 'process.stdout.write("compiled\\npages: 3"); process.stderr.write("a warning\\n")'],
      tmpdir(),
      { PATH: process.env.PATH ?? "" },
      { print: (line) => printed.push(line) },
    );

    expect(printed.filter((line) => line !== "a warning")).toEqual(["compiled", "pages: 3"]);
    expect(printed).toContain("a warning");
  });

  it("prints a line too long to hold whole in pieces, losing none of it", async () => {
    const printed: string[] = [];

    await buildProcess.spawn(
      "node",
      ["-e", 'process.stdout.write("x".repeat(200000) + "\\n")'],
      tmpdir(),
      { PATH: process.env.PATH ?? "" },
      { print: (line) => printed.push(line) },
    );

    expect(printed.length).toBeGreaterThan(1);
    expect(printed.join("")).toBe("x".repeat(200000));
  });
});

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

    const hosting = await runAdapterBuild(built, adapter, { OCEL_BUILD_ID: "b1" }, quiet);

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

    const refused = runAdapterBuild(app(), adapter, {}, quiet);

    await expect(refused).rejects.toThrow(
      /nothing wrote hosting\.json.*@ocel\/sveltekit\. Add it as the adapter/,
    );
    await expect(refused).rejects.toBeInstanceOf(Refusal);
  });

  for (const body of ["null", "[]", '"sveltekit"']) {
    it(`refuses hosting.json holding ${body}, naming the file`, async () => {
      const built = app();
      buildProcess.spawn = async () =>
        writeFileSync(path.join(built.outputDir, "hosting.json"), body);

      await expect(runAdapterBuild(built, adapter, {}, quiet)).rejects.toThrow(
        /hosting\.json it wrote is not an object/,
      );
    });
  }

  it("refuses hosting another adapter wrote", async () => {
    const built = app();
    buildProcess.spawn = async () =>
      writeFileSync(path.join(built.outputDir, "hosting.json"), '{"framework":"node"}');

    await expect(runAdapterBuild(built, adapter, {}, quiet)).rejects.toThrow(
      /names framework "node"/,
    );
  });

  it("refuses hosting of a version this CLI does not read, saying how to match it", async () => {
    const built = app();
    buildProcess.spawn = async () =>
      writeFileSync(
        path.join(built.outputDir, "hosting.json"),
        '{"version":2,"framework":"sveltekit"}',
      );

    const refused = runAdapterBuild(built, adapter, {}, quiet);

    await expect(refused).rejects.toThrow(/version 2.*Install the @ocel\/sveltekit release/);
    await expect(refused).rejects.toBeInstanceOf(Refusal);
  });
});
