import { createHash } from "node:crypto";
import { createReadStream, createWriteStream } from "node:fs";
import { mkdir, readdir, stat, writeFile } from "node:fs/promises";
import { dirname, join, relative, sep } from "node:path";
import { pipeline } from "node:stream/promises";
import type { BuildOutput } from "@framework/node-build/output";
import type { Hosting } from "@platform/edge-contract/hosting";
import type { BuildContext, BuildPlan } from "./build-plan.mjs";

const NEXT_STATIC_SEGMENT = "/_next/static/";

export function deriveNextStatic(pathnames: string[]): NonNullable<Hosting["static"]> {
  const immutablePrefixes = [
    ...new Set(
      pathnames.flatMap((pathname) => {
        const at = pathname.indexOf(NEXT_STATIC_SEGMENT);
        return at === -1 ? [] : [pathname.slice(0, at + NEXT_STATIC_SEGMENT.length)];
      }),
    ),
  ].sort();
  return {
    immutablePrefixes,
    mustRevalidatePrefixes: immutablePrefixes.map((prefix) => `${prefix}service-worker/`),
  };
}

export function servedPathname(file: { pathname: string; filePath: string }): string {
  return file.filePath.endsWith(".html") && !file.pathname.endsWith(".html")
    ? `${file.pathname}.html`
    : file.pathname;
}

async function copyHashedFile(src: string, dest: string): Promise<string> {
  await mkdir(dirname(dest), { recursive: true });
  const hash = createHash("sha256");
  await pipeline(
    createReadStream(src),
    async function* (chunks) {
      for await (const chunk of chunks) {
        hash.update(chunk);
        yield chunk;
      }
    },
    createWriteStream(dest, { mode: (await stat(src)).mode }),
  );
  return hash.digest("hex");
}

async function writeHashedFile(dest: string, content: string): Promise<string> {
  await mkdir(dirname(dest), { recursive: true });
  await writeFile(dest, content);
  return createHash("sha256").update(content).digest("hex");
}

async function collectPublicFiles(
  projectDir: string,
): Promise<{ pathname: string; filePath: string }[]> {
  const publicDir = join(projectDir, "public");
  let entries;
  try {
    entries = await readdir(publicDir, {
      recursive: true,
      withFileTypes: true,
    });
  } catch {
    return [];
  }
  const files: { pathname: string; filePath: string }[] = [];
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    const abs = join(entry.parentPath, entry.name);
    const rel = relative(publicDir, abs);
    files.push({ pathname: `/${rel.split(sep).join("/")}`, filePath: abs });
  }
  return files;
}

export interface StaticAssets {
  publicFiles: { pathname: string; filePath: string }[];
  assetHashes: Record<string, string>;
}

export async function writeStaticAssets(
  ctx: BuildContext,
  plan: BuildPlan,
  output: BuildOutput,
): Promise<StaticAssets> {
  const publicFiles = await collectPublicFiles(ctx.projectDir);
  const staticAssets = [
    ...publicFiles.map((f) => [f.pathname, f.filePath] as const),
    ...ctx.outputs.staticFiles.map((f) => [servedPathname(f), f.filePath] as const),
  ];
  const assetHashes: Record<string, string> = {};
  for (const [pathname, filePath] of staticAssets) {
    assetHashes[pathname] = await copyHashedFile(filePath, join(output.staticDir, pathname));
  }
  for (const { dataKey } of plan.nextDataStaticFiles) {
    assetHashes[dataKey] = await writeHashedFile(join(output.staticDir, dataKey), "{}");
  }
  return { publicFiles, assetHashes };
}
