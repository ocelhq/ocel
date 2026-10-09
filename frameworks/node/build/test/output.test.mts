import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readlinkSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import layout from "../fixtures/build-output.json" with { type: "json" };
import {
  BuildOutput,
  FUNCTION_CONFIG_FILE,
  FUNCTION_DIR_SUFFIX,
  FUNCTIONS_DIR,
  HOSTING_FILE,
  HOSTING_VERSION,
  ROOT_FUNCTION,
  ROOT_FUNCTION_DIR,
  STATIC_DIR,
} from "../src/output.mjs";

function scratch(): string {
  return mkdtempSync(path.join(tmpdir(), "ocel-build-output-"));
}

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("the build output layout", () => {
  it("is the layout the CLI reads", () => {
    expect({
      hostingFile: HOSTING_FILE,
      hostingVersion: HOSTING_VERSION,
      functionConfigFile: FUNCTION_CONFIG_FILE,
      functionsDir: FUNCTIONS_DIR,
      functionDirSuffix: FUNCTION_DIR_SUFFIX,
      rootFunction: ROOT_FUNCTION,
      rootFunctionDir: ROOT_FUNCTION_DIR,
      staticDir: STATIC_DIR,
    }).toEqual(layout);
  });
});

describe("BuildOutput", () => {
  it("writes where the CLI says and names the app it says", () => {
    vi.stubEnv("OCEL_OUTPUT_DIR", "/out/apps/web");
    vi.stubEnv("OCEL_APP_NAME", "web");

    const output = BuildOutput.fromEnv({ dir: "build", app: "fallback" });

    expect(output.dir).toBe("/out/apps/web");
    expect(output.app).toBe("web");
  });

  it("writes to the adapter's own directory, resolved, when built outside ocel", () => {
    vi.stubEnv("OCEL_OUTPUT_DIR", "");
    vi.stubEnv("OCEL_APP_NAME", "");

    const output = BuildOutput.fromEnv({ dir: "build", app: "fallback" });

    expect(output.dir).toBe(path.resolve("build"));
    expect(output.app).toBe("fallback");
  });

  it("puts the root function in index.func and every other function under its id", () => {
    const output = new BuildOutput("/out", "web");

    expect(output.functionDir(ROOT_FUNCTION)).toBe("/out/functions/index.func");
    expect(output.functionDir("bundle-0")).toBe("/out/functions/bundle-0.func");
    expect(output.staticDir).toBe("/out/static");
  });

  it("states each function's framework, entry, id and app", () => {
    const output = new BuildOutput(scratch(), "web");
    mkdirSync(output.functionDir("bundle-0"), { recursive: true });

    output.writeFunctionConfig("bundle-0", "next", "app/__next_launcher.cjs");

    expect(
      JSON.parse(
        readFileSync(path.join(output.functionDir("bundle-0"), "function-config.json"), "utf8"),
      ),
    ).toEqual({
      framework: { name: "next" },
      entryFile: "app/__next_launcher.cjs",
      id: "bundle-0",
      app: "web",
    });
  });

  it("stamps hosting with the version this release reads", () => {
    const output = new BuildOutput(scratch(), "web");

    output.writeHosting({
      framework: "sveltekit",
      frameworkBuildId: "v1",
      rootFunction: ROOT_FUNCTION,
      needs: {},
    });

    expect(JSON.parse(readFileSync(path.join(output.dir, "hosting.json"), "utf8"))).toEqual({
      version: 1,
      framework: "sveltekit",
      frameworkBuildId: "v1",
      rootFunction: "/",
      needs: {},
    });
  });

  it("removes what an earlier build wrote and leaves the rest of the directory", () => {
    const output = new BuildOutput(scratch(), "web");
    mkdirSync(output.functionDir(ROOT_FUNCTION), { recursive: true });
    mkdirSync(output.staticDir, { recursive: true });
    output.writeFile("hosting.json", "{}");
    output.writeFile("README.md", "mine");

    output.clean();

    expect(existsSync(path.join(output.dir, "functions"))).toBe(false);
    expect(existsSync(output.staticDir)).toBe(false);
    expect(existsSync(path.join(output.dir, "hosting.json"))).toBe(false);
    expect(readFileSync(path.join(output.dir, "README.md"), "utf8")).toBe("mine");
  });

  it("links a symlinked asset to its copy inside the function when the function carries the target", async () => {
    const root = scratch();
    mkdirSync(path.join(root, "vendor/pkg"), { recursive: true });
    writeFileSync(path.join(root, "vendor/pkg/index.js"), "module.exports = 1");
    mkdirSync(path.join(root, "node_modules"));
    symlinkSync(path.join(root, "vendor/pkg"), path.join(root, "node_modules/pkg"));
    const output = new BuildOutput(path.join(root, "out"), "web");

    await output.copyIntoFunction(
      "bundle-0",
      {
        "vendor/pkg": path.join(root, "vendor/pkg"),
        "node_modules/pkg": path.join(root, "node_modules/pkg"),
      },
      root,
    );

    const dest = path.join(output.functionDir("bundle-0"), "node_modules/pkg");
    expect(readlinkSync(dest)).toBe("../vendor/pkg");
    expect(readFileSync(path.join(dest, "index.js"), "utf8")).toBe("module.exports = 1");
  });

  it("refuses an asset that would land outside its function, writing nothing there", async () => {
    const root = scratch();
    writeFileSync(path.join(root, "secret.txt"), "x");
    const output = new BuildOutput(path.join(root, "out"), "web");

    await expect(
      output.copyIntoFunction(
        "bundle-0",
        { "../escaped.txt": path.join(root, "secret.txt") },
        root,
      ),
    ).rejects.toThrow(/outside the function/);
    expect(existsSync(path.join(output.dir, "functions", "escaped.txt"))).toBe(false);
  });

  it("leaves out an asset with no source on disk", async () => {
    const root = scratch();
    const output = new BuildOutput(path.join(root, "out"), "web");

    await output.copyIntoFunction("bundle-0", { "gone.js": path.join(root, "gone.js") }, root);

    expect(existsSync(path.join(output.functionDir("bundle-0"), "gone.js"))).toBe(false);
  });

  it.skipIf(process.getuid?.() === 0)(
    "fails rather than leave out an asset it cannot read",
    async () => {
      const root = scratch();
      mkdirSync(path.join(root, "locked"));
      writeFileSync(path.join(root, "locked/index.js"), "x");
      chmodSync(path.join(root, "locked"), 0o000);
      const output = new BuildOutput(path.join(root, "out"), "web");

      try {
        await expect(
          output.copyIntoFunction(
            "bundle-0",
            { "locked/index.js": path.join(root, "locked/index.js") },
            root,
          ),
        ).rejects.toThrow(/EACCES/);
      } finally {
        chmodSync(path.join(root, "locked"), 0o755);
      }
    },
  );
});
