import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { serveNextContainer } from "../src/container-entry.mjs";
import { getNextHost } from "../src/host.mjs";
import {
  withLauncherConfig,
  writeNextProjectFixture,
} from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = { async handler(req, res) { res.end("rendered"); } };
`;

let dir: string;
let launcherPath: string;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-container-"));
  const projectDir = join(dir, "project");
  const fixture = await writeNextProjectFixture(projectDir);
  launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, withLauncherConfig(launcherModule, fixture.nextConfig));
});

afterAll(async () => {
  delete process.env.PORT;
  delete process.env.OCEL_HANDLER;
  await rm(dir, { recursive: true, force: true });
});

test("a Next container refuses to start where nothing names its port", async () => {
  delete process.env.PORT;

  await expect(serveNextContainer({})).rejects.toThrow(/PORT/);
});

test("a Next container serves the app on every network at the port its platform names", async () => {
  const port = await freePort();
  process.env.PORT = String(port);
  process.env.OCEL_HANDLER = launcherPath;

  await serveNextContainer({ cacheTagsPerObject: 7 });

  expect(getNextHost().bind).toEqual({ host: "0.0.0.0", port });
  expect(getNextHost().cacheTagsPerObject).toBe(7);
  expect(await answer(port)).toBe("rendered");
});

async function answer(port: number): Promise<string> {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      return await (await fetch(`http://127.0.0.1:${port}/`)).text();
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
  throw new Error(`nothing answered on ${port}`);
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.on("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address() as net.AddressInfo;
      probe.close(() => resolve(port));
    });
  });
}
