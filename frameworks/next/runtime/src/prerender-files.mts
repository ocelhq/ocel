import { readFile, stat } from "node:fs/promises";
import { join } from "node:path";
import { type CacheEntryFile, routeOf } from "@framework/next-cache";

type Revalidate = number | false | undefined;

interface PrerenderManifest {
  routes?: Record<string, { initialRevalidateSeconds?: Revalidate; initialExpireSeconds?: number }>;
  dynamicRoutes?: Record<
    string,
    { fallback?: unknown; fallbackRevalidate?: Revalidate; fallbackExpire?: number }
  >;
}

interface PrerenderMeta {
  headers?: Record<string, unknown>;
  status?: number;
  postponed?: string;
  segmentPaths?: string[];
}

const manifests = new Map<string, Promise<PrerenderManifest | null>>();

function readManifest(serverDistDir: string): Promise<PrerenderManifest | null> {
  let manifest = manifests.get(serverDistDir);
  if (!manifest) {
    manifest = readFile(join(serverDistDir, "..", "prerender-manifest.json"), "utf8").then(
      (raw) => JSON.parse(raw) as PrerenderManifest,
      () => null,
    );
    manifests.set(serverDistDir, manifest);
  }
  return manifest;
}

function cacheControlOf(
  manifest: PrerenderManifest,
  route: string,
): CacheEntryFile["cacheControl"] | null {
  const prerendered = manifest.routes?.[route];
  if (prerendered) {
    return windowOf(prerendered.initialRevalidateSeconds, prerendered.initialExpireSeconds);
  }
  const dynamic = manifest.dynamicRoutes?.[route];
  if (typeof dynamic?.fallback === "string") {
    return windowOf(dynamic.fallbackRevalidate, dynamic.fallbackExpire);
  }
  return null;
}

function windowOf(revalidate: Revalidate, expire: number | undefined) {
  return revalidate === undefined ? {} : { revalidate, ...(expire !== undefined && { expire }) };
}

async function readMeta(file: string): Promise<PrerenderMeta | undefined> {
  try {
    return JSON.parse(await readFile(file, "utf8"));
  } catch {
    return undefined;
  }
}

async function readSegments(
  segmentsDir: string,
  segmentPaths: string[],
): Promise<Record<string, string>> {
  const segments: Record<string, string> = {};
  await Promise.all(
    segmentPaths.map(async (path) => {
      try {
        segments[path] = (await readFile(`${segmentsDir}${path}.segment.rsc`)).toString("base64");
      } catch {}
    }),
  );
  return segments;
}

async function readAppPage(base: string, ctx: any): Promise<Record<string, any>> {
  const html = await readFile(`${base}.html`, "utf8");
  const meta = await readMeta(`${base}.meta`);
  const value: Record<string, any> = {
    kind: "APP_PAGE",
    html,
    headers: meta?.headers,
    status: meta?.status,
  };
  if (meta?.postponed !== undefined) value.postponed = meta.postponed;
  if (!ctx?.isFallback && (!ctx?.isRoutePPREnabled || meta?.postponed == null)) {
    value.rscData = (await readFile(`${base}.rsc`)).toString("base64");
  }
  if (meta?.segmentPaths) {
    value.segmentData = await readSegments(`${base}.segments`, meta.segmentPaths);
  }
  return value;
}

async function readPagesPage(base: string, ctx: any): Promise<Record<string, any>> {
  const html = await readFile(`${base}.html`, "utf8");
  const pageData = ctx?.isFallback ? {} : JSON.parse(await readFile(`${base}.json`, "utf8"));
  return { kind: "PAGES", html, pageData };
}

async function readAppRoute(base: string): Promise<Record<string, any>> {
  const body = await readFile(`${base}.body`);
  const meta = await readMeta(`${base}.meta`);
  return {
    kind: "APP_ROUTE",
    body: body.toString("base64"),
    headers: meta?.headers,
    status: meta?.status,
  };
}

const prerenderFiles: Record<
  string,
  { dir: string; served: string; read: (base: string, ctx: any) => Promise<Record<string, any>> }
> = {
  APP_PAGE: { dir: "app", served: ".html", read: readAppPage },
  APP_ROUTE: { dir: "app", served: ".body", read: readAppRoute },
  PAGES: { dir: "pages", served: ".html", read: readPagesPage },
};

export async function readPrerender(
  serverDistDir: string,
  key: string,
  ctx: any,
): Promise<CacheEntryFile | null> {
  const files = prerenderFiles[ctx?.kind];
  if (!files) return null;
  const manifest = await readManifest(serverDistDir);
  const cacheControl = manifest && cacheControlOf(manifest, routeOf(key));
  if (!cacheControl) return null;

  const base = join(serverDistDir, files.dir, key);
  try {
    const { mtime } = await stat(`${base}${files.served}`);
    return { lastModified: mtime.getTime(), value: await files.read(base, ctx), cacheControl };
  } catch {
    return null;
  }
}
