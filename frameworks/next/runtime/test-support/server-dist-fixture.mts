import { mkdirSync, mkdtempSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

export interface ServerDistFixture {
  serverDistDir: string;
  remove(): void;
}

export function writeServerDistFixture(
  manifest: { routes?: Record<string, unknown>; dynamicRoutes?: Record<string, unknown> },
  files: Record<string, string | Buffer>,
  writtenAt = Date.now() - 10_000,
): ServerDistFixture {
  const distDir = mkdtempSync(join(tmpdir(), "ocel-next-dist-"));
  writeFileSync(
    join(distDir, "prerender-manifest.json"),
    JSON.stringify({ version: 4, routes: {}, dynamicRoutes: {}, notFoundRoutes: [], ...manifest }),
  );
  const serverDistDir = join(distDir, "server");
  for (const [path, body] of Object.entries(files)) {
    const file = join(serverDistDir, path);
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(file, body);
    utimesSync(file, writtenAt / 1000, writtenAt / 1000);
  }
  return { serverDistDir, remove: () => rmSync(distDir, { recursive: true, force: true }) };
}
