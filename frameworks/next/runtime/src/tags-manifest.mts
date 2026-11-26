import { createRequire } from "node:module";
import { join } from "node:path";
import type { TagRecord } from "@platform/edge-contract/purge";

interface Mark {
  stale?: number;
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

export function mirrorTag(tag: string, record: TagRecord): void {
  const manifest = mirror().manifest;
  if (!manifest || record.stale === undefined) return;
  manifest.set(tag, { stale: Math.max(manifest.get(tag)?.stale ?? -Infinity, record.stale) });
}
