import { type ChildProcess, execFile, spawn } from "node:child_process";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { isBuiltin } from "node:module";
import net from "node:net";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import { writeNextProjectFixture } from "@framework/next-runtime/test-support/next-project-fixture";
import { init, parse } from "es-module-lexer";
import { afterAll, afterEach, beforeAll, expect, test } from "vitest";

const execFileAsync = promisify(execFile);
const pkgDir = resolve(import.meta.dirname, "..");

const children: ChildProcess[] = [];
let dist: string;
let dir: string;

beforeAll(async () => {
  dist = await mkdtemp(join(tmpdir(), "ocel-gcp-next-"));
  await execFileAsync("bun", ["scripts/bundle-next.mjs", relative(pkgDir, dist)], {
    cwd: pkgDir,
  });
  dir = join(dist, "next");
}, 120_000);

afterAll(async () => {
  await rm(dist, { recursive: true, force: true });
});

afterEach(() => {
  for (const child of children.splice(0)) child.kill("SIGKILL");
});

async function shippedFiles(): Promise<string[]> {
  const entries = await readdir(dir, { recursive: true, withFileTypes: true });
  return entries
    .filter((entry) => entry.isFile())
    .map((entry) => relative(dir, join(entry.parentPath, entry.name)))
    .sort();
}

function freePort(): Promise<number> {
  return new Promise((done, fail) => {
    const probe = net.createServer();
    probe.on("error", fail);
    probe.listen({ host: "127.0.0.1", port: 0 }, () => {
      const { port } = probe.address() as net.AddressInfo;
      probe.close(() => done(port));
    });
  });
}

async function answer(port: number): Promise<string> {
  for (let attempt = 0; attempt < 200; attempt++) {
    try {
      return await (await fetch(`http://127.0.0.1:${port}/`)).text();
    } catch {
      await new Promise((wait) => setTimeout(wait, 25));
    }
  }
  throw new Error(`nothing answered on port ${port}`);
}

test("the runtime directory holds the entrypoint and every cache handler a Next build names", async () => {
  expect(await shippedFiles()).toEqual(
    expect.arrayContaining([
      "cache-handler.cjs",
      "entrypoint.mjs",
      "use-cache-default.cjs",
      "use-cache-remote.cjs",
    ]),
  );
});

test("the entrypoint imports nothing but Node's own modules and the sharp the directory ships", async () => {
  await init;
  const [imports] = parse(await readFile(join(dir, "entrypoint.mjs"), "utf8"));
  const bare = imports
    .flatMap((found) => (found.n === undefined ? [] : [found.n]))
    .filter((specifier) => !isBuiltin(specifier));
  expect([...new Set(bare)]).toEqual(["sharp"]);
});

test("the runtime directory ships sharp built for the Linux x64 Cloud Run runs", async () => {
  expect(await shippedFiles()).toEqual(
    expect.arrayContaining([
      "node_modules/sharp/package.json",
      expect.stringMatching(/^node_modules\/@img\/sharp-linux-x64\/lib\/sharp-linux-x64.*\.node$/),
      expect.stringMatching(/^node_modules\/@img\/sharp-libvips-linux-x64\/lib\/libvips-cpp\.so/),
    ]),
  );
});

test("no file in the runtime directory contains a path of the checkout it was built in", async () => {
  const checkout = resolve(pkgDir, "..", "..", "..");
  const leaking: string[] = [];
  for (const file of await shippedFiles()) {
    if ((await readFile(join(dir, file), "utf8")).includes(checkout)) leaking.push(file);
  }
  expect(leaking).toEqual([]);
});

const staleLauncher = (log: string) => `const { appendFileSync } = require("node:fs");
module.exports = {
  async handler(req, res) {
    if (req.headers["x-ocel-refresh"]) {
      await new Promise((done) => setTimeout(done, 200));
      appendFileSync(${JSON.stringify(log)}, "refreshed " + req.url + "\\n");
      res.end("fresh");
      return;
    }
    req.headers[Symbol.for("ocel.next.stale-entry.v1")] = 1000;
    res.setHeader("x-nextjs-cache", "STALE");
    res.end("stale");
  },
};
`;

test("a stale page served by a Next service billed per request is re-rendered before its response ends", async () => {
  const projectDir = join(dist, "stale-project");
  await writeNextProjectFixture(
    projectDir,
    {},
    { routes: { "/blog": { initialRevalidateSeconds: 60 } } },
  );
  const log = join(projectDir, "refreshes.log");
  await writeFile(log, "");
  const launcher = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcher, staleLauncher(log));
  const manifest = join(projectDir, "routing-manifest.json");
  await writeFile(
    manifest,
    JSON.stringify({
      entry: "bundle-0",
      buildId: "b1",
      basePath: "",
      pathnames: ["/blog"],
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      },
      dispatch: { "/blog": { kind: "function", id: "bundle-0", entryKey: "/blog" } },
    }),
  );
  const port = await freePort();

  const child = spawn(process.execPath, [join(dir, "entrypoint.mjs")], {
    cwd: projectDir,
    env: {
      PATH: process.env.PATH,
      OCEL_HANDLER: launcher,
      PORT: String(port),
      OCEL_ORIGIN_DISPATCH: "1",
      OCEL_ORIGIN_SIGNED: "1",
      OCEL_ROUTING_MANIFEST: manifest,
      OCEL_FINISH_BEFORE_RESPONSE_MS: "5000",
    },
    stdio: ["ignore", "inherit", "inherit"],
  });
  children.push(child);
  await answer(port);
  await writeFile(log, "");

  const res = await fetch(`http://127.0.0.1:${port}/blog`);

  expect(await res.text()).toBe("stale");
  expect(await readFile(log, "utf8")).toBe("refreshed /blog\n");
});

test("the entrypoint serves a Next app on the port Cloud Run names", async () => {
  const projectDir = join(dist, "project");
  await writeNextProjectFixture(projectDir);
  const launcher = join(projectDir, "__next_launcher.cjs");
  await writeFile(
    launcher,
    `module.exports = { async handler(req, res) { res.end("rendered"); } };\n`,
  );
  const port = await freePort();

  const child = spawn(process.execPath, [join(dir, "entrypoint.mjs")], {
    cwd: projectDir,
    env: { PATH: process.env.PATH, OCEL_HANDLER: launcher, PORT: String(port) },
    stdio: ["ignore", "inherit", "inherit"],
  });
  children.push(child);

  expect(await answer(port)).toBe("rendered");
});
