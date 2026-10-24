import { lstat, mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { routeCacheKey } from "@framework/next-cache/naming";
import { boundCacheTags, type CacheTagLimits } from "@platform/edge-contract/cache-tags";
import type { AdapterOutput } from "next";

export function cacheTags(
  prerender: AdapterOutput["PRERENDER"],
  limits: CacheTagLimits | undefined,
): string[] {
  const header = prerender.fallback?.initialHeaders?.["x-next-cache-tags"];
  const raw = Array.isArray(header) ? header.join(",") : header;
  if (!raw) return [];
  if (!limits) return [...new Set(raw.split(",").filter(Boolean))];

  const { tags, dropped } = boundCacheTags(raw.split(","), limits);
  if (dropped > 0) {
    console.warn(
      `ocel: dropped ${dropped} cache tag(s) over the edge's cache tag limits for "${prerender.pathname}" — purges naming them will not hit this route`,
    );
  }
  return tags;
}

interface CacheEntryFile {
  lastModified: number;
  value: Record<string, unknown>;
}

function segmentPath(pathname: string): string | null {
  const at = pathname.indexOf(".segments/");
  if (at === -1 || !pathname.endsWith(".segment.rsc")) return null;
  return pathname.slice(at + ".segments".length, -".segment.rsc".length);
}

async function readMaybe(filePath: string | undefined): Promise<Buffer | null> {
  if (!filePath) return null;
  try {
    return await readFile(filePath);
  } catch {
    return null;
  }
}

export interface PrerenderGroup {
  entryKey: string;
  html: any;
  rsc: any;
  data: any;
  segments: any[];
}

const NEXT_DATA_PRERENDER = /\/_next\/data\/[^/]+\/.*\.json$/;

export function isUserFacingPathname(pathname: string): boolean {
  return (
    segmentPath(pathname) === null &&
    !pathname.endsWith(".rsc") &&
    !NEXT_DATA_PRERENDER.test(pathname)
  );
}

export function groupPrerenders(prerenders: readonly any[]): PrerenderGroup[] {
  const byGroup = new Map<number, any[]>();
  for (const p of prerenders) {
    const members = byGroup.get(p.groupId);
    if (members) members.push(p);
    else byGroup.set(p.groupId, [p]);
  }

  const groups: PrerenderGroup[] = [];
  for (const members of byGroup.values()) {
    const segments = members.filter((m) => segmentPath(m.pathname) !== null);
    const pages = members.filter((m) => segmentPath(m.pathname) === null);
    const html = pages.find((m) => isUserFacingPathname(m.pathname));
    if (!html) continue;
    groups.push({
      entryKey: routeCacheKey(html.pathname),
      html,
      rsc: pages.find((m) => m.pathname.endsWith(".rsc")),
      data: pages.find((m) => NEXT_DATA_PRERENDER.test(m.pathname)),
      segments,
    });
  }
  return groups;
}

function variantHeadersOf(group: PrerenderGroup): Record<string, unknown> {
  const rscHeaders = group.rsc?.fallback?.initialHeaders;
  const segmentHeaders = group.segments.find((m) => m.fallback?.initialHeaders)?.fallback
    .initialHeaders;
  return {
    ...(rscHeaders && { rscHeaders }),
    ...(segmentHeaders && { segmentHeaders }),
  };
}

export function variantHeaderProjection(
  groups: readonly PrerenderGroup[],
): Record<string, Record<string, unknown>> {
  const projection: Record<string, Record<string, unknown>> = {};
  for (const group of groups) {
    const headers = variantHeadersOf(group);
    if (Object.keys(headers).length > 0) projection[group.entryKey] = headers;
  }
  return projection;
}

export async function emitCacheEntries(
  outputRoot: string,
  groups: readonly PrerenderGroup[],
  kindById: ReadonlyMap<string, string>,
): Promise<void> {
  const lastModified = Date.now();

  await Promise.all(
    groups.map(async (group) => {
      const { entryKey, html, rsc, data, segments } = group;
      const body = await readMaybe(html.fallback?.filePath);
      if (!body) return;

      const kind = kindById.get(html.parentOutputId) ?? "APP_PAGE";

      const value: Record<string, unknown> = {
        kind,
        status: html.fallback.initialStatus,
      };

      if (kind === "APP_ROUTE") {
        value.headers = html.fallback.initialHeaders;
        value.body = body.toString("base64");
      } else {
        value.headers = html.fallback.initialHeaders;
        value.html = body.toString("utf8");
        const rscBody = await readMaybe(rsc?.fallback?.filePath);
        if (rscBody) value.rscData = rscBody.toString("base64");
        const dataBody = await readMaybe(data?.fallback?.filePath);
        if (dataBody) value.pageData = JSON.parse(dataBody.toString("utf8"));
        Object.assign(value, variantHeadersOf(group));

        const segmentData: Record<string, string> = {};
        for (const m of segments) {
          const segBody = await readMaybe(m.fallback?.filePath);
          if (segBody) {
            segmentData[segmentPath(m.pathname)!] = segBody.toString("base64");
          }
        }
        if (Object.keys(segmentData).length > 0) value.segmentData = segmentData;
        if (html.fallback.postponedState !== undefined) {
          value.postponed = html.fallback.postponedState;
        }
      }

      const dest = join(outputRoot, "cache", `${entryKey}.cache.json`);
      await mkdir(dirname(dest), { recursive: true });
      const entry: CacheEntryFile = { lastModified, value };
      await writeFile(dest, JSON.stringify(entry));
    }),
  );
}

export async function emitFetchEntries(outputRoot: string, distDir: string): Promise<void> {
  const fetchCacheDir = join(distDir, "cache", "fetch-cache");
  let names: string[];
  try {
    names = await readdir(fetchCacheDir);
  } catch {
    return;
  }

  const lastModified = Date.now();

  await Promise.all(
    names.map(async (name) => {
      const src = join(fetchCacheDir, name);
      const [raw, stats] = await Promise.all([
        readFile(src, "utf8").catch(() => null),
        lstat(src).catch(() => null),
      ]);
      if (raw === null || !stats?.isFile()) return;

      let value: Record<string, unknown>;
      try {
        value = JSON.parse(raw);
      } catch {
        return;
      }

      const revalidate = value.revalidate;
      if (typeof revalidate === "number" && stats.mtimeMs + revalidate * 1000 <= lastModified) {
        return;
      }

      const dest = join(outputRoot, "fetch-cache", `${name}.cache.json`);
      await mkdir(dirname(dest), { recursive: true });
      const entry: CacheEntryFile = { lastModified, value };
      await writeFile(dest, JSON.stringify(entry));
    }),
  );
}
