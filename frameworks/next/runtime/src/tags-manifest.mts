import { createRequire } from "node:module";
import { join } from "node:path";

interface Mark {
  stale?: number;
  expired?: number;
}

type Manifest = Map<string, Mark>;

interface Mirror {
  manifest: Manifest | null;
}

const stateKey = Symbol.for("ocel.next.tags-manifest.v1");

const manifestModule = "next/dist/server/lib/incremental-cache/tags-manifest.external.js";

function mirror(): Mirror {
  const host = globalThis as Record<symbol, Mirror | undefined>;
  return (host[stateKey] ??= { manifest: null });
}

function unmirrored(reason: string): null {
  console.warn(`ocel: tag staleness will not reach Next's manifest: ${reason}`);
  return null;
}

export function loadTagsManifest(projectDir: string): Manifest | null {
  let manifest: unknown;
  try {
    manifest = createRequire(join(projectDir, "package.json"))(manifestModule)?.tagsManifest;
  } catch (err) {
    return unmirrored(`${manifestModule} did not resolve from ${projectDir}: ${String(err)}`);
  }
  return manifest instanceof Map
    ? manifest
    : unmirrored(`${manifestModule} exports no tagsManifest Map`);
}

export function mirrorTagsInto(manifest: Manifest | null): void {
  mirror().manifest = manifest;
}

function latest(a: number | undefined, b: number | undefined): number | undefined {
  if (a === undefined) return b;
  if (b === undefined) return a;
  return Math.max(a, b);
}

export function mirrorTag(tag: string, record: Mark): void {
  const manifest = mirror().manifest;
  if (!manifest) return;
  const existing = manifest.get(tag);
  manifest.set(tag, {
    stale: latest(existing?.stale, record.stale),
    expired: latest(existing?.expired, record.expired),
  });
}
