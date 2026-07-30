import { lstatSync, readdirSync } from "node:fs";
import { join } from "node:path";

// AWS Lambda's ceiling on an unzipped direct-upload artifact.
export const defaultBudgetBytes = 200 * 1024 * 1024;

export interface PackedMember<T> {
  member: T;
  // Every asset this member names, sized as the copy will land it. Its own
  // traced weight, not its delta in the bundle: bundle-mates share nearly all
  // of their assets, so a delta says more about packing order than the member.
  sizeBytes: number;
}

export interface Bundle<T> {
  name: string;
  members: PackedMember<T>[];
  // The union of every member's assets: dest-key relative to the repo root,
  // absolute source path on disk.
  assets: Record<string, string>;
  sizeBytes: number;
}

export interface PackResult<T> {
  bundles: Bundle<T>[];
  // Dest keys whose source is not on disk: charged nothing and packed anyway,
  // since the copy will skip them too. Returned rather than reported so the
  // copy site owns the single aggregate warning per build.
  missingAssets: string[];
}

export interface PackOptions<T> {
  entryKeyOf: (member: T) => string;
  assetsOf: (member: T) => Record<string, string>;
  // Members that cannot share one Lambda. Defaults to a single partition:
  // Next's maxDuration/preferredRegion are per-function settings, so once they
  // are plumbed through they become this key and the packer stays unchanged.
  partitionBy?: (member: T) => string;
  budgetBytes?: number;
  // undefined for a path that does not exist, which is charged nothing and
  // named in the result rather than costing a silent zero.
  sizeOf?: (absPath: string) => number | undefined;
}

// packBundles packs members into the fewest Lambda artifacts their assets fit
// in. Routes in a Next build share nearly all of their traced assets — the
// node_modules forest and the chunk set — so a bundle's cost is the union of its
// members' assets and a second route usually costs only its delta. Members are
// packed in entry-key order and bundles named in the order they are opened, so
// identical input always yields an identical artifact.
//
// Two members that map one dest key to different sources cannot share a bundle,
// so the second one opens a new bundle and gets its own copy.
export function packBundles<T>(
  members: readonly T[],
  opts: PackOptions<T>,
): PackResult<T> {
  const {
    entryKeyOf,
    assetsOf,
    partitionBy = () => "",
    budgetBytes = defaultBudgetBytes,
    sizeOf = sizeOfPath,
  } = opts;

  assertUniqueEntryKeys(members, entryKeyOf);

  const sizes = new Map<string, number | undefined>();
  const sizeOfCached = (abs: string) => {
    if (!sizes.has(abs)) sizes.set(abs, sizeOf(abs));
    return sizes.get(abs);
  };
  const missing = new Set<string>();

  const partitions = new Map<string, T[]>();
  for (const member of [...members].sort(byKey(entryKeyOf))) {
    const key = partitionBy(member);
    const group = partitions.get(key);
    if (group) group.push(member);
    else partitions.set(key, [member]);
  }

  const bundles: Bundle<T>[] = [];
  const open = (): Bundle<T> => {
    const bundle = {
      name: `bundle-${bundles.length}`,
      members: [],
      assets: {},
      sizeBytes: 0,
    };
    bundles.push(bundle);
    return bundle;
  };

  const bytesOf = (entries: [string, string][]) =>
    entries.reduce((sum, [, abs]) => sum + (sizeOfCached(abs) ?? 0), 0);

  const absorb = (bundle: Bundle<T>, assets: Record<string, string>) => {
    for (const [dest, abs] of Object.entries(assets)) {
      if (dest in bundle.assets) continue;
      bundle.assets[dest] = abs;
      const size = sizeOfCached(abs);
      if (size === undefined) missing.add(dest);
      else bundle.sizeBytes += size;
    }
  };

  for (const key of [...partitions.keys()].sort()) {
    let bundle = open();

    for (const member of partitions.get(key)!) {
      const assets = assetsOf(member);
      const entries = Object.entries(assets);
      const conflicts = entries.some(
        ([dest, abs]) => dest in bundle.assets && bundle.assets[dest] !== abs,
      );
      const delta = bytesOf(
        entries.filter(([dest]) => !(dest in bundle.assets)),
      );

      if (
        bundle.members.length > 0 &&
        (conflicts || bundle.sizeBytes + delta > budgetBytes)
      ) {
        bundle = open();
      }
      absorb(bundle, assets);
      bundle.members.push({ member, sizeBytes: bytesOf(entries) });

      // Nothing can be packed with a member that overflows the budget alone, so
      // it ships as-is and AWS rejects the artifact with its own clear error.
      if (bundle.members.length === 1 && bundle.sizeBytes > budgetBytes) {
        console.warn(
          `ocel: "${entryKeyOf(member)}" traces ${bundle.sizeBytes} bytes of assets, over the ${budgetBytes}-byte function limit — it cannot be split any further`,
        );
        bundle = open();
      }
    }

    if (bundle.members.length === 0) bundles.pop();
  }

  return { bundles, missingAssets: [...missing].sort() };
}

function byKey<T>(entryKeyOf: (member: T) => string) {
  return (a: T, b: T) => {
    const left = entryKeyOf(a);
    const right = entryKeyOf(b);
    return left < right ? -1 : left > right ? 1 : 0;
  };
}

// Entry keys index the launcher's route table, so a duplicate would silently
// drop one route and serve another route's module in its place.
function assertUniqueEntryKeys<T>(
  members: readonly T[],
  entryKeyOf: (member: T) => string,
): void {
  const seen = new Set<string>();
  for (const member of members) {
    const key = entryKeyOf(member);
    if (seen.has(key)) {
      throw new Error(`ocel: two members share the entry key "${key}"`);
    }
    seen.add(key);
  }
}

// A path's size on disk as the asset copy will land it: a directory asset is
// copied whole, so it costs its recursive contents; a symlink is preserved as a
// link and costs only itself, with its target copied under its own asset entry.
// A path that is not there has no size, which the packer returns.
function sizeOfPath(absPath: string): number | undefined {
  let info;
  try {
    info = lstatSync(absPath);
  } catch {
    return undefined;
  }
  if (!info.isDirectory()) return info.size;

  let total = 0;
  for (const entry of readdirSync(absPath, { withFileTypes: true })) {
    total += sizeOfPath(join(absPath, entry.name)) ?? 0;
  }
  return total;
}
